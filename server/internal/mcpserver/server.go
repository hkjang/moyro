// Package mcpserver exposes moyro collaboration data through the official MCP
// Go SDK and the current stateless Streamable HTTP transport.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"

	"github.com/hkjang/moyro/server/internal/approval"
	"github.com/hkjang/moyro/server/internal/channels"
	"github.com/hkjang/moyro/server/internal/posts"
	"github.com/hkjang/moyro/server/internal/rbac"
	"github.com/hkjang/moyro/server/internal/teams"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type UserIDResolver func(context.Context) string
type CredentialIDResolver func(context.Context) string
type CredentialPermissionChecker func(context.Context, string) bool
type Authorizer func(context.Context, string, string, string) (bool, error)
type ApprovedAuthorizer func(context.Context, string, string, string, string, string) (bool, error)
type AuditLogger func(context.Context, string, string, string, string, error)

type Dependencies struct {
	Teams             *teams.Service
	Channels          *channels.Service
	Posts             *posts.Service
	Approval          *approval.Service
	UserID            UserIDResolver
	CredentialID      CredentialIDResolver
	CredentialAllows  CredentialPermissionChecker
	Authorize         Authorizer
	AuthorizeApproved ApprovedAuthorizer
	Audit             AuditLogger
	Version           string
}

type Service struct {
	deps    Dependencies
	server  *mcp.Server
	handler http.Handler
	policy  atomic.Pointer[Policy]
}

type Policy struct {
	AllowedTools     map[string]struct{}
	AllowedResources []string
}

func New(deps Dependencies) (*Service, error) {
	if deps.Teams == nil || deps.Channels == nil || deps.Posts == nil || deps.UserID == nil {
		return nil, errors.New("mcpserver: incomplete dependencies")
	}
	if deps.Authorize == nil {
		return nil, errors.New("mcpserver: authorizer is required")
	}
	if deps.Approval != nil && (deps.CredentialID == nil || deps.CredentialAllows == nil || deps.AuthorizeApproved == nil) {
		return nil, errors.New("mcpserver: approval credential resolver and deferred authorizer are required")
	}
	if deps.Version == "" {
		deps.Version = "dev"
	}
	service := &Service{deps: deps}
	service.ConfigurePolicy(nil, nil)
	service.server = mcp.NewServer(&mcp.Implementation{
		Name: "moyro", Title: "moyro collaboration server", Version: deps.Version,
		Description: "Permission-aware access to moyro teams, channels, messages, and reviews.",
	}, nil)
	service.registerTools()
	service.registerResources()

	streamable := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return service.server
	}, &mcp.StreamableHTTPOptions{
		Stateless:                    true,
		JSONResponse:                 false,
		MaxRequestBodyBytes:          2 << 20,
		PropagateRequestCancellation: true,
	})
	service.handler = http.NewCrossOriginProtection().Handler(streamable)
	return service, nil
}

func (s *Service) Handler() http.Handler { return s.handler }

// ConfigurePolicy atomically replaces the runtime allowlist. Empty slices
// intentionally deny all tools/resources; administrators must opt in.
func (s *Service) ConfigurePolicy(tools, resources []string) {
	policy := &Policy{AllowedTools: map[string]struct{}{}, AllowedResources: append([]string(nil), resources...)}
	for _, tool := range tools {
		tool = strings.TrimSpace(tool)
		if tool != "" {
			policy.AllowedTools[tool] = struct{}{}
		}
	}
	s.policy.Store(policy)
}

func (s *Service) allowTool(name string) error {
	policy := s.policy.Load()
	if policy == nil {
		return errors.New("MCP policy is unavailable")
	}
	if _, ok := policy.AllowedTools[name]; !ok {
		return fmt.Errorf("MCP tool %s is disabled by administrator policy", name)
	}
	return nil
}

func (s *Service) allowResource(uri string) error {
	policy := s.policy.Load()
	if policy == nil {
		return errors.New("MCP policy is unavailable")
	}
	for _, prefix := range policy.AllowedResources {
		prefix = strings.TrimSuffix(strings.TrimSpace(prefix), "/")
		if uri == prefix || strings.HasPrefix(uri, prefix+"/") {
			return nil
		}
	}
	return errors.New("MCP resource is disabled by administrator policy")
}

type emptyInput struct{}

type listTeamsOutput struct {
	Teams []teams.Team `json:"teams"`
}

type listChannelsInput struct {
	TeamID string `json:"team_id" jsonschema:"ID of a team the current user belongs to"`
}

type listChannelsOutput struct {
	Channels []channels.Channel `json:"channels"`
}

type searchMessagesInput struct {
	TeamID    string `json:"team_id" jsonschema:"Team to search"`
	Query     string `json:"query" jsonschema:"Plain-text search terms"`
	ChannelID string `json:"channel_id,omitempty" jsonschema:"Optional channel restriction"`
	Page      int    `json:"page,omitempty" jsonschema:"Zero-based result page"`
	PerPage   int    `json:"per_page,omitempty" jsonschema:"Results per page, maximum 100"`
}

type searchMessagesOutput struct {
	Result *posts.SearchResult `json:"result"`
}

type getThreadInput struct {
	PostID string `json:"post_id" jsonschema:"Root post ID"`
}

type getThreadOutput struct {
	Thread *posts.PostList `json:"thread"`
}

type createPostInput struct {
	ChannelID      string `json:"channel_id" jsonschema:"Destination channel ID"`
	Message        string `json:"message" jsonschema:"Message body"`
	RootID         string `json:"root_id,omitempty" jsonschema:"Root post ID when replying"`
	IdempotencyKey string `json:"idempotency_key,omitempty" jsonschema:"Unique client key used to avoid duplicate review requests"`
}

// approvedPostPayload records the exact restricted credential that submitted
// a deferred action. The key ID is populated by the server and is not part of
// the MCP tool input schema, so clients cannot substitute another credential.
// Deferred execution reloads this key and intersects its current grants with
// the requester's current RBAC permissions.
type approvedPostPayload struct {
	ChannelID      string `json:"channel_id"`
	Message        string `json:"message"`
	RootID         string `json:"root_id,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	CredentialID   string `json:"_moyro_credential_id"`
}

func makeApprovedPostPayload(input createPostInput, credentialID string) approvedPostPayload {
	return approvedPostPayload{
		ChannelID: input.ChannelID, Message: input.Message, RootID: input.RootID,
		IdempotencyKey: input.IdempotencyKey, CredentialID: credentialID,
	}
}

func (p approvedPostPayload) input() createPostInput {
	return createPostInput{
		ChannelID: p.ChannelID, Message: p.Message, RootID: p.RootID,
		IdempotencyKey: p.IdempotencyKey,
	}
}

type createPostOutput struct {
	ApprovalRequired bool              `json:"approval_required"`
	ApprovalRequest  *approval.Request `json:"approval_request,omitempty"`
	Post             *posts.Post       `json:"post,omitempty"`
}

type listApprovalsInput struct {
	Status string `json:"status,omitempty" jsonschema:"Optional pending, approved, rejected, expired, or executed filter"`
	Limit  int    `json:"limit,omitempty" jsonschema:"Maximum number of requests, up to 200"`
}

type listApprovalsOutput struct {
	Requests []approval.Request `json:"requests"`
}

type decideApprovalInput struct {
	RequestID string `json:"request_id" jsonschema:"Approval request ID"`
	Reason    string `json:"reason,omitempty" jsonschema:"Reviewer reason"`
}

type decideApprovalOutput struct {
	Request *approval.Request `json:"request"`
}

func (s *Service) registerTools() {
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: boolPointer(false)}
	write := &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: boolPointer(false), OpenWorldHint: boolPointer(false), IdempotentHint: true}
	review := &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: boolPointer(true), OpenWorldHint: boolPointer(false), IdempotentHint: true}

	mcp.AddTool(s.server, &mcp.Tool{
		Name: "list_teams", Title: "List my teams", Description: "Lists teams visible to the authenticated moyro user.", Annotations: readOnly,
	}, s.listTeams)
	mcp.AddTool(s.server, &mcp.Tool{
		Name: "list_channels", Title: "List team channels", Description: "Lists channels the authenticated user belongs to in a team.", Annotations: readOnly,
	}, s.listChannels)
	mcp.AddTool(s.server, &mcp.Tool{
		Name: "search_messages", Title: "Search messages", Description: "Full-text search over messages in channels visible to the authenticated user.", Annotations: readOnly,
	}, s.searchMessages)
	mcp.AddTool(s.server, &mcp.Tool{
		Name: "get_thread", Title: "Read a thread", Description: "Reads a thread only when the user belongs to its channel.", Annotations: readOnly,
	}, s.getThread)
	mcp.AddTool(s.server, &mcp.Tool{
		Name: "create_post", Title: "Create a post", Description: "Creates a channel post or returns a pending review when an approval policy applies.", Annotations: write,
	}, s.createPost)
	mcp.AddTool(s.server, &mcp.Tool{
		Name: "reply_to_thread", Title: "Reply to a thread", Description: "Creates a reply or returns a pending review when an approval policy applies.", Annotations: write,
	}, s.replyToThread)
	if s.deps.Approval != nil {
		mcp.AddTool(s.server, &mcp.Tool{
			Name: "list_pending_approvals", Title: "List pending reviews", Description: "Lists review requests the authenticated reviewer may decide.", Annotations: readOnly,
		}, s.listPendingApprovals)
		mcp.AddTool(s.server, &mcp.Tool{
			Name: "approve_request", Title: "Approve a request", Description: "Records an approval decision with the configured reviewer permission.", Annotations: review,
		}, s.approveRequest)
		mcp.AddTool(s.server, &mcp.Tool{
			Name: "reject_request", Title: "Reject a request", Description: "Rejects a pending request. The reason is retained for the requester.", Annotations: review,
		}, s.rejectRequest)
	}
}

func (s *Service) registerResources() {
	s.server.AddResourceTemplate(&mcp.ResourceTemplate{
		Name: "moyro-team", Title: "moyro team", Description: "A team visible to the authenticated user.",
		MIMEType: "application/json", URITemplate: "moyro://teams/{id}",
	}, s.readResource)
	s.server.AddResourceTemplate(&mcp.ResourceTemplate{
		Name: "moyro-channel", Title: "moyro channel", Description: "A channel the authenticated user belongs to.",
		MIMEType: "application/json", URITemplate: "moyro://channels/{id}",
	}, s.readResource)
	s.server.AddResourceTemplate(&mcp.ResourceTemplate{
		Name: "moyro-thread", Title: "moyro thread", Description: "A message thread in a visible channel.",
		MIMEType: "application/json", URITemplate: "moyro://threads/{id}",
	}, s.readResource)
}

func (s *Service) listTeams(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, listTeamsOutput, error) {
	if err := s.allowTool("list_teams"); err != nil {
		return nil, listTeamsOutput{}, err
	}
	uid, err := s.require(ctx, rbac.PermissionMCPRead, "", "")
	if err != nil {
		return nil, listTeamsOutput{}, err
	}
	list, err := s.deps.Teams.ListForUser(ctx, uid)
	if err == nil {
		filtered := list[:0]
		for _, team := range list {
			allowed, authErr := s.deps.Authorize(ctx, rbac.PermissionMCPRead, "team", team.ID)
			if authErr != nil {
				err = authErr
				break
			}
			if allowed {
				filtered = append(filtered, team)
			}
		}
		list = filtered
	}
	s.audit(ctx, uid, "list_teams", "", "", err)
	return nil, listTeamsOutput{Teams: list}, err
}

func (s *Service) listChannels(ctx context.Context, _ *mcp.CallToolRequest, input listChannelsInput) (*mcp.CallToolResult, listChannelsOutput, error) {
	if err := s.allowTool("list_channels"); err != nil {
		return nil, listChannelsOutput{}, err
	}
	uid, err := s.require(ctx, rbac.PermissionMCPRead, "team", input.TeamID)
	if err != nil {
		return nil, listChannelsOutput{}, err
	}
	list, err := s.deps.Channels.ListForUser(ctx, uid, input.TeamID)
	if err == nil {
		filtered := list[:0]
		for _, channel := range list {
			allowed, authErr := s.deps.Authorize(ctx, rbac.PermissionMCPRead, "channel", channel.ID)
			if authErr != nil {
				err = authErr
				break
			}
			if allowed {
				filtered = append(filtered, channel)
			}
		}
		list = filtered
	}
	s.audit(ctx, uid, "list_channels", "team", input.TeamID, err)
	return nil, listChannelsOutput{Channels: list}, err
}

func (s *Service) searchMessages(ctx context.Context, _ *mcp.CallToolRequest, input searchMessagesInput) (*mcp.CallToolResult, searchMessagesOutput, error) {
	if err := s.allowTool("search_messages"); err != nil {
		return nil, searchMessagesOutput{}, err
	}
	resourceType, resourceID := "team_search", input.TeamID
	if input.ChannelID != "" {
		resourceType, resourceID = "channel", input.ChannelID
	}
	uid, err := s.require(ctx, rbac.PermissionMCPRead, resourceType, resourceID)
	if err != nil {
		return nil, searchMessagesOutput{}, err
	}
	if strings.TrimSpace(input.Query) == "" || input.TeamID == "" {
		return nil, searchMessagesOutput{}, errors.New("team_id and query are required")
	}
	result, err := s.deps.Posts.Search(ctx, uid, input.TeamID, input.Query, posts.SearchFilters{InChannelID: input.ChannelID}, input.Page, input.PerPage)
	s.audit(ctx, uid, "search_messages", "team", input.TeamID, err)
	return nil, searchMessagesOutput{Result: result}, err
}

func (s *Service) getThread(ctx context.Context, _ *mcp.CallToolRequest, input getThreadInput) (*mcp.CallToolResult, getThreadOutput, error) {
	if err := s.allowTool("get_thread"); err != nil {
		return nil, getThreadOutput{}, err
	}
	uid, err := s.require(ctx, rbac.PermissionMCPRead, "post", input.PostID)
	if err != nil {
		return nil, getThreadOutput{}, err
	}
	root, err := s.deps.Posts.Get(ctx, input.PostID)
	if err != nil || root == nil {
		return nil, getThreadOutput{}, errors.New("thread not found")
	}
	member, err := s.deps.Channels.IsMember(ctx, root.ChannelID, uid)
	if err != nil || !member {
		return nil, getThreadOutput{}, errors.New("thread is not visible to this user")
	}
	thread, err := s.deps.Posts.ListThread(ctx, input.PostID)
	s.audit(ctx, uid, "get_thread", "post", input.PostID, err)
	return nil, getThreadOutput{Thread: thread}, err
}

func (s *Service) createPost(ctx context.Context, _ *mcp.CallToolRequest, input createPostInput) (*mcp.CallToolResult, createPostOutput, error) {
	if err := s.allowTool("create_post"); err != nil {
		return nil, createPostOutput{}, err
	}
	return s.createPostCommon(ctx, "create_post", input)
}

func (s *Service) replyToThread(ctx context.Context, _ *mcp.CallToolRequest, input createPostInput) (*mcp.CallToolResult, createPostOutput, error) {
	if err := s.allowTool("reply_to_thread"); err != nil {
		return nil, createPostOutput{}, err
	}
	if input.RootID == "" {
		return nil, createPostOutput{}, errors.New("root_id is required")
	}
	return s.createPostCommon(ctx, "reply_to_thread", input)
}

func (s *Service) createPostCommon(ctx context.Context, tool string, input createPostInput) (*mcp.CallToolResult, createPostOutput, error) {
	uid, err := s.require(ctx, rbac.PermissionMCPWrite, "channel", input.ChannelID)
	if err != nil {
		return nil, createPostOutput{}, err
	}
	if input.ChannelID == "" || strings.TrimSpace(input.Message) == "" {
		return nil, createPostOutput{}, errors.New("channel_id and message are required")
	}
	member, err := s.deps.Channels.IsMember(ctx, input.ChannelID, uid)
	if err != nil || !member {
		return nil, createPostOutput{}, errors.New("channel is not visible to this user")
	}
	channel, err := s.deps.Channels.Get(ctx, input.ChannelID)
	if err != nil || channel == nil {
		return nil, createPostOutput{}, errors.New("channel not found")
	}
	if input.RootID != "" {
		root, err := s.deps.Posts.Get(ctx, input.RootID)
		if err != nil || root == nil || root.DeleteAt != 0 || root.RootID != "" || root.ChannelID != input.ChannelID {
			return nil, createPostOutput{}, errors.New("root post is not in the destination channel")
		}
	}
	if s.deps.Approval != nil {
		required, err := s.deps.Approval.Required(ctx, "mcp."+tool, channel.TeamID)
		if err != nil {
			return nil, createPostOutput{}, err
		}
		if required {
			if _, err := s.require(ctx, rbac.PermissionRequestApproval, "team", channel.TeamID); err != nil {
				return nil, createPostOutput{}, err
			}
		}
		payload := any(input)
		if required {
			credentialID := strings.TrimSpace(s.deps.CredentialID(ctx))
			if credentialID == "" {
				return nil, createPostOutput{}, errors.New("approval requires an authenticated MCP credential")
			}
			payload = makeApprovedPostPayload(input, credentialID)
		}
		result, err := s.deps.Approval.Submit(ctx, approval.Submission{
			ActionType: "mcp." + tool, RequesterID: uid, TeamID: channel.TeamID,
			ResourceType: "channel", ResourceID: input.ChannelID,
			Payload: payload, IdempotencyKey: input.IdempotencyKey,
		})
		if err != nil {
			return nil, createPostOutput{}, err
		}
		if result.ApprovalRequired {
			s.audit(ctx, uid, tool, "approval_request", result.Request.ID, nil)
			return nil, createPostOutput{ApprovalRequired: true, ApprovalRequest: result.Request}, nil
		}
	}
	post, err := s.deps.Posts.Create(ctx, input.ChannelID, uid, input.RootID, input.Message, map[string]any{"from_mcp": true}, nil)
	s.audit(ctx, uid, tool, "channel", input.ChannelID, err)
	return nil, createPostOutput{Post: post}, err
}

func (s *Service) listPendingApprovals(ctx context.Context, _ *mcp.CallToolRequest, input listApprovalsInput) (*mcp.CallToolResult, listApprovalsOutput, error) {
	if err := s.allowTool("list_pending_approvals"); err != nil {
		return nil, listApprovalsOutput{}, err
	}
	uid := s.deps.UserID(ctx)
	if uid == "" || s.deps.CredentialAllows == nil || !s.deps.CredentialAllows(ctx, rbac.PermissionReviewApproval) {
		return nil, listApprovalsOutput{}, errors.New("permission denied: review_approval")
	}
	status := input.Status
	if status == "" {
		status = "pending"
	}
	list, err := s.deps.Approval.List(ctx, uid, true, status, input.Limit)
	if err == nil {
		list, err = s.filterAuthorizedApprovals(ctx, list)
	}
	s.audit(ctx, uid, "list_pending_approvals", "", "", err)
	return nil, listApprovalsOutput{Requests: list}, err
}

func (s *Service) filterAuthorizedApprovals(ctx context.Context, requests []approval.Request) ([]approval.Request, error) {
	filtered := make([]approval.Request, 0, len(requests))
	for _, request := range requests {
		allowed, err := s.deps.Authorize(ctx, rbac.PermissionReviewApproval, "approval_request", request.ID)
		if err != nil {
			return nil, err
		}
		if allowed {
			filtered = append(filtered, request)
		}
	}
	return filtered, nil
}

func (s *Service) approveRequest(ctx context.Context, _ *mcp.CallToolRequest, input decideApprovalInput) (*mcp.CallToolResult, decideApprovalOutput, error) {
	if err := s.allowTool("approve_request"); err != nil {
		return nil, decideApprovalOutput{}, err
	}
	return s.decide(ctx, "approve", input)
}

func (s *Service) rejectRequest(ctx context.Context, _ *mcp.CallToolRequest, input decideApprovalInput) (*mcp.CallToolResult, decideApprovalOutput, error) {
	if err := s.allowTool("reject_request"); err != nil {
		return nil, decideApprovalOutput{}, err
	}
	return s.decide(ctx, "reject", input)
}

func (s *Service) decide(ctx context.Context, decision string, input decideApprovalInput) (*mcp.CallToolResult, decideApprovalOutput, error) {
	uid, err := s.require(ctx, rbac.PermissionReviewApproval, "approval_request", input.RequestID)
	if err != nil {
		return nil, decideApprovalOutput{}, err
	}
	request, err := s.deps.Approval.Decide(ctx, input.RequestID, uid, decision, input.Reason)
	if err == nil && request != nil && request.Status == "approved" {
		_, request, err = s.ExecuteApproved(ctx, request)
	}
	s.audit(ctx, uid, decision+"_request", "approval_request", input.RequestID, err)
	return nil, decideApprovalOutput{Request: request}, err
}

// ExecuteApproved applies the side effect represented by an approved MCP
// request and then closes it as executed. Native REST review handlers call the
// same method, keeping approval semantics independent of the reviewing client.
func (s *Service) ExecuteApproved(ctx context.Context, request *approval.Request) (*posts.Post, *approval.Request, error) {
	if request == nil || request.Status != "approved" {
		return nil, request, nil
	}
	if request.ActionType != "mcp.create_post" && request.ActionType != "mcp.reply_to_thread" {
		return nil, request, nil
	}
	tool := strings.TrimPrefix(request.ActionType, "mcp.")
	if err := s.allowTool(tool); err != nil {
		return nil, request, err
	}
	var payload approvedPostPayload
	if err := json.Unmarshal(request.Payload, &payload); err != nil {
		return nil, request, fmt.Errorf("decode approved MCP action: %w", err)
	}
	input := payload.input()
	if strings.TrimSpace(payload.CredentialID) == "" || s.deps.AuthorizeApproved == nil {
		return nil, request, errors.New("approved MCP action has no verifiable credential provenance")
	}
	if input.ChannelID == "" || strings.TrimSpace(input.Message) == "" ||
		request.ResourceType != "channel" || request.ResourceID != input.ChannelID {
		return nil, request, errors.New("approved MCP action payload does not match its protected resource")
	}
	if request.ActionType == "mcp.reply_to_thread" && input.RootID == "" {
		return nil, request, errors.New("approved MCP reply has no root post")
	}
	for _, permission := range []string{rbac.PermissionMCPWrite, rbac.PermissionRequestApproval} {
		allowed, err := s.deps.AuthorizeApproved(ctx, request.RequesterID, payload.CredentialID, permission, "channel", input.ChannelID)
		if err != nil {
			return nil, request, fmt.Errorf("revalidate approved MCP action: %w", err)
		}
		if !allowed {
			return nil, request, errors.New("approval requester or originating credential no longer has permission for the channel")
		}
	}
	member, err := s.deps.Channels.IsMember(ctx, input.ChannelID, request.RequesterID)
	if err != nil || !member {
		return nil, request, errors.New("approval requester no longer has access to the channel")
	}
	channel, err := s.deps.Channels.Get(ctx, input.ChannelID)
	if err != nil || channel == nil || (request.TeamID != "" && channel.TeamID != request.TeamID) {
		return nil, request, errors.New("approved destination channel is no longer valid")
	}
	if input.RootID != "" {
		root, err := s.deps.Posts.Get(ctx, input.RootID)
		if err != nil || root == nil || root.ChannelID != input.ChannelID {
			return nil, request, errors.New("approved root post is no longer valid")
		}
	}
	post, err := s.deps.Posts.GetByApprovalRequest(ctx, request.ID)
	if err != nil {
		post, err = s.deps.Posts.Create(ctx, input.ChannelID, request.RequesterID, input.RootID, input.Message, map[string]any{
			"from_mcp": true, "approval_request_id": request.ID,
		}, nil)
		if err != nil {
			// A concurrent retry may have won the partial unique index.
			if existing, lookupErr := s.deps.Posts.GetByApprovalRequest(ctx, request.ID); lookupErr == nil {
				post, err = existing, nil
			}
		}
	}
	if err != nil {
		return nil, request, err
	}
	executed, err := s.deps.Approval.MarkExecuted(ctx, request.ID)
	if err != nil {
		return post, request, err
	}
	return post, executed, nil
}

func (s *Service) readResource(ctx context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	if err := s.allowResource(request.Params.URI); err != nil {
		return nil, err
	}
	parsed, err := url.Parse(request.Params.URI)
	if err != nil || parsed.Scheme != "moyro" {
		return nil, errors.New("invalid moyro resource URI")
	}
	id := strings.TrimPrefix(parsed.Path, "/")
	resourceType := ""
	switch parsed.Host {
	case "teams":
		resourceType = "team"
	case "channels":
		resourceType = "channel"
	case "threads":
		resourceType = "post"
	default:
		return nil, errors.New("unknown moyro resource type")
	}
	uid, err := s.require(ctx, rbac.PermissionMCPRead, resourceType, id)
	if err != nil {
		return nil, err
	}
	var value any
	switch parsed.Host {
	case "teams":
		visible, err := s.deps.Teams.IsMember(ctx, id, uid)
		if err != nil || !visible {
			return nil, errors.New("team resource not found")
		}
		value, err = s.deps.Teams.Get(ctx, id)
	case "channels":
		visible, err := s.deps.Channels.IsMember(ctx, id, uid)
		if err != nil || !visible {
			return nil, errors.New("channel resource not found")
		}
		value, err = s.deps.Channels.Get(ctx, id)
	case "threads":
		root, getErr := s.deps.Posts.Get(ctx, id)
		if getErr != nil || root == nil {
			return nil, errors.New("thread resource not found")
		}
		visible, memberErr := s.deps.Channels.IsMember(ctx, root.ChannelID, uid)
		if memberErr != nil || !visible {
			return nil, errors.New("thread resource not found")
		}
		value, err = s.deps.Posts.ListThread(ctx, id)
	}
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	s.audit(ctx, uid, "read_resource", parsed.Host, id, nil)
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
		URI: request.Params.URI, MIMEType: "application/json", Text: string(body),
	}}}, nil
}

func (s *Service) require(ctx context.Context, permission, resourceType, resourceID string) (string, error) {
	uid := s.deps.UserID(ctx)
	if uid == "" {
		return "", errors.New("authentication is required")
	}
	allowed, err := s.deps.Authorize(ctx, permission, resourceType, resourceID)
	if err != nil {
		return "", err
	}
	if !allowed {
		return "", fmt.Errorf("permission %s is required", permission)
	}
	return uid, nil
}

func (s *Service) audit(ctx context.Context, userID, tool, resourceType, resourceID string, err error) {
	if s.deps.Audit != nil {
		s.deps.Audit(ctx, userID, tool, resourceType, resourceID, err)
	}
}

func boolPointer(value bool) *bool { return &value }
