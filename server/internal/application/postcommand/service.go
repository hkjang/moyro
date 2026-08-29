// Package postcommand owns the application-level lifecycle for creating a
// post. Transport adapters translate their request into a Command and retain
// responsibility for protocol-specific error/status mapping.
package postcommand

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/hkjang/moyro/server/internal/audit"
	"github.com/hkjang/moyro/server/internal/auth"
	"github.com/hkjang/moyro/server/internal/channels"
	"github.com/hkjang/moyro/server/internal/links"
	"github.com/hkjang/moyro/server/internal/posts"
	"github.com/hkjang/moyro/server/internal/ws"
)

// Source identifies the trusted adapter that submitted a command. The service
// derives server-owned provenance props from this value rather than trusting
// identically named keys in a transport payload.
type Source string

const (
	SourceREST            Source = "rest"
	SourceMCP             Source = "mcp"
	SourceScheduled       Source = "scheduled"
	SourceIncomingWebhook Source = "incoming_webhook"
	SourceSlashCommand    Source = "slash_command"
)

// Command is the transport-neutral input required to create a post.
type Command struct {
	Source    Source
	ActorID   string
	ChannelID string
	RootID    string
	Message   string
	Props     map[string]any
	FileIDs   []string
	// CredentialID is a non-secret server-side row identifier (for example a
	// session or API-key id) included in audit metadata only. It is never copied
	// into post props or returned to message clients.
	CredentialID string
	// ApprovalRequestID is server-owned replay metadata. Transport adapters
	// must never copy it from an untrusted post props map.
	ApprovalRequestID string
	// ScheduledPostID is persisted in the dedicated posts.scheduled_post_id
	// column and provides replay safety for a leased scheduled delivery.
	ScheduledPostID string
	// Incoming webhook presentation metadata is derived from the authenticated
	// hook and its payload, never from an arbitrary post props map.
	OverrideUsername string
	OverrideIconURL  string
	SenderName       string
	// WebhookDepth is trusted loop metadata propagated by Moyro outgoing
	// deliveries when they target an incoming hook. Transport-supplied post
	// props cannot set it directly.
	WebhookDepth int
	// SlashCommand identifies the built-in that synthesized the message. It is
	// currently used to preserve the /me rendering marker.
	SlashCommand string
}

// FailureCode lets the HTTP adapter preserve its established Mattermost error
// ids and status codes without moving protocol concerns into this package.
type FailureCode string

const (
	FailureMembershipCheck  FailureCode = "membership_check"
	FailureNotMember        FailureCode = "not_member"
	FailurePermissionCheck  FailureCode = "permission_check"
	FailurePermissionDenied FailureCode = "permission_denied"
	FailureInvalidRoot      FailureCode = "invalid_root"
	FailurePluginRejected   FailureCode = "plugin_rejected"
	FailureSave             FailureCode = "save"
)

// Failure retains the original error text while adding an application-level
// category for adapters. Error intentionally delegates to Cause so existing
// HTTP response messages remain byte-for-byte compatible.
type Failure struct {
	Code  FailureCode
	Cause error
}

func (e *Failure) Error() string {
	if e == nil || e.Cause == nil {
		return "post command failed"
	}
	return e.Cause.Error()
}

func (e *Failure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// FailureCodeOf returns the application failure category or the empty string
// for an error that did not originate in this service.
func FailureCodeOf(err error) FailureCode {
	var failure *Failure
	if errors.As(err, &failure) {
		return failure.Code
	}
	return ""
}

type ChannelService interface {
	IsMember(ctx context.Context, channelID, userID string) (bool, error)
	BumpUnread(ctx context.Context, channelID, authorID string, mentionedIDs []string) ([]channels.Counter, error)
}

type PostStore interface {
	Get(ctx context.Context, id string) (*posts.Post, error)
	Create(ctx context.Context, channelID, userID, rootID, message string, props map[string]any, fileIDs []string) (*posts.Post, error)
	CreateScheduled(ctx context.Context, scheduledPostID, channelID, userID, rootID, message string, props map[string]any, fileIDs []string) (*posts.Post, error)
	UpdateFileIDs(ctx context.Context, postID string, fileIDs []string) error
	UpdateLinkMetadata(ctx context.Context, postID string, previews []posts.LinkPreview) error
}

type FileAssociator interface {
	AssociateWithPost(ctx context.Context, ownerID string, fileIDs []string, postID, channelID string) ([]string, error)
}

type UserDirectory interface {
	UserIDsByUsernames(ctx context.Context, names []string) (map[string]string, error)
	UserByID(ctx context.Context, id string) (*auth.User, error)
}

type BotResolver interface {
	IsBot(ctx context.Context, userID string) (bool, error)
}

type PluginHooks interface {
	MessageWillBePosted(ctx context.Context, post []byte) (modified []byte, rejected bool, reason string)
	MessageHasBeenPosted(ctx context.Context, post []byte)
}

type EventSink interface {
	Broadcast(event ws.Event)
}

type OutgoingDispatcher interface {
	Dispatch(ctx context.Context, post *posts.Post, authorUsername string)
}

type LinkPreviewer interface {
	Fetch(ctx context.Context, rawURL string) posts.LinkPreview
}

type AuditSink interface {
	LogAsync(actorID, action, target string, payload map[string]any)
}

type Dependencies struct {
	Channels           ChannelService
	Posts              PostStore
	Files              FileAssociator
	Users              UserDirectory
	Bots               BotResolver
	Plugins            PluginHooks
	Events             EventSink
	Outgoing           OutgoingDispatcher
	LinkPreviews       LinkPreviewer
	Audit              AuditSink
	AuthorizeCreate    func(ctx context.Context, actorID, channelID string) (bool, error)
	Logger             *slog.Logger
	IncrementPostCount func()
}

type Service struct {
	channels           ChannelService
	posts              PostStore
	files              FileAssociator
	users              UserDirectory
	bots               BotResolver
	plugins            PluginHooks
	events             EventSink
	outgoing           OutgoingDispatcher
	linkPreviews       LinkPreviewer
	audit              AuditSink
	authorizeCreate    func(ctx context.Context, actorID, channelID string) (bool, error)
	logger             *slog.Logger
	incrementPostCount func()
}

func New(deps Dependencies) *Service {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	increment := deps.IncrementPostCount
	if increment == nil {
		increment = func() {}
	}
	return &Service{
		channels: deps.Channels, posts: deps.Posts, files: deps.Files,
		users: deps.Users, bots: deps.Bots, plugins: deps.Plugins,
		events: deps.Events, outgoing: deps.Outgoing, linkPreviews: deps.LinkPreviews, audit: deps.Audit,
		authorizeCreate: deps.AuthorizeCreate,
		logger:          logger, incrementPostCount: increment,
	}
}

// Execute applies the common create-post lifecycle for every trusted adapter.
// Best-effort post-commit work logs failures but does not turn an already
// persisted post into a failed transport request.
func (s *Service) Execute(ctx context.Context, command Command) (*posts.Post, error) {
	if s.authorizeCreate != nil {
		allowed, err := s.authorizeCreate(ctx, command.ActorID, command.ChannelID)
		if err != nil {
			return nil, fail(FailurePermissionCheck, err)
		}
		if !allowed {
			return nil, fail(FailurePermissionDenied, errors.New("create_post permission is required"))
		}
	}
	isMember, err := s.channels.IsMember(ctx, command.ChannelID, command.ActorID)
	if err != nil {
		return nil, fail(FailureMembershipCheck, err)
	}
	if !isMember {
		return nil, fail(FailureNotMember, errors.New("not a channel member"))
	}
	if command.RootID != "" {
		root, err := s.posts.Get(ctx, command.RootID)
		if err != nil || root == nil || root.DeleteAt != 0 || root.RootID != "" || root.ChannelID != command.ChannelID {
			return nil, fail(FailureInvalidRoot, posts.ErrInvalidRoot)
		}
	}

	// Plugin hooks may inspect and replace message/props. File ids intentionally
	// remain absent from this provisional object to preserve the existing hook
	// contract during this behavior-preserving extraction.
	props := applyServerMetadata(command, stripReservedProps(command.Props))
	provisional := posts.Post{
		ChannelID: command.ChannelID,
		UserID:    command.ActorID,
		RootID:    command.RootID,
		Message:   command.Message,
		Props:     props,
	}
	message := command.Message
	if s.plugins != nil {
		provisionalRaw, _ := json.Marshal(provisional)
		modified, rejected, reason := s.plugins.MessageWillBePosted(ctx, provisionalRaw)
		if rejected {
			if reason == "" {
				reason = "post rejected by plugin"
			}
			return nil, fail(FailurePluginRejected, errors.New(reason))
		}
		if len(modified) > 0 {
			var modifiedPost posts.Post
			if err := json.Unmarshal(modified, &modifiedPost); err == nil {
				message = modifiedPost.Message
				if modifiedPost.Props != nil {
					props = modifiedPost.Props
				}
			}
		}
	}
	// Server workflow and credential metadata must not be forgeable through a
	// transport payload or a message hook. Normal Mattermost props pass through.
	props = applyServerMetadata(command, stripReservedProps(props))

	post, err := s.create(ctx, command, message, props)
	if err != nil {
		if errors.Is(err, posts.ErrInvalidRoot) {
			return nil, fail(FailureInvalidRoot, err)
		}
		return nil, fail(FailureSave, err)
	}
	s.incrementPostCount()
	s.auditCreated(command, post)

	s.associateFiles(ctx, command, post)
	mentionIDs := s.resolveMentions(ctx, post.Message)
	raw := s.broadcastCreated(ctx, command, post, mentionIDs)
	s.notifyPlugins(raw)
	s.dispatchOutgoing(ctx, post)
	s.fetchLinkPreviews(post)

	return post, nil
}

func (s *Service) auditCreated(command Command, post *posts.Post) {
	if s.audit == nil || post == nil {
		return
	}
	payload := map[string]any{
		"source":     string(command.Source),
		"channel_id": post.ChannelID,
	}
	if post.RootID != "" {
		payload["root_id"] = post.RootID
	}
	if credentialID := strings.TrimSpace(command.CredentialID); credentialID != "" {
		payload["credential_id"] = credentialID
	}
	if approvalRequestID := strings.TrimSpace(command.ApprovalRequestID); approvalRequestID != "" {
		payload["approval_request_id"] = approvalRequestID
	}
	if scheduledPostID := strings.TrimSpace(command.ScheduledPostID); scheduledPostID != "" {
		payload["scheduled_post_id"] = scheduledPostID
	}
	s.audit.LogAsync(post.UserID, audit.ActionPostCreate, post.ID, payload)
}

func fail(code FailureCode, cause error) error {
	return &Failure{Code: code, Cause: cause}
}

func (s *Service) create(ctx context.Context, command Command, message string, props map[string]any) (*posts.Post, error) {
	if command.Source == SourceScheduled {
		return s.posts.CreateScheduled(
			ctx, strings.TrimSpace(command.ScheduledPostID), command.ChannelID,
			command.ActorID, command.RootID, message, props, command.FileIDs,
		)
	}
	return s.posts.Create(ctx, command.ChannelID, command.ActorID, command.RootID, message, props, command.FileIDs)
}

func (s *Service) associateFiles(ctx context.Context, command Command, post *posts.Post) {
	if s.files == nil || len(command.FileIDs) == 0 {
		return
	}
	attached, err := s.files.AssociateWithPost(ctx, command.ActorID, command.FileIDs, post.ID, post.ChannelID)
	if err != nil {
		s.logger.Warn("file associate", "post", post.ID, "err", err)
		return
	}
	post.FileIDs = attached
	if err := s.posts.UpdateFileIDs(ctx, post.ID, attached); err != nil {
		s.logger.Warn("post update file_ids", "post", post.ID, "err", err)
	}
}

func (s *Service) resolveMentions(ctx context.Context, message string) []string {
	mentionIDs := []string{}
	if s.users == nil {
		return mentionIDs
	}
	names := ExtractMentions(message)
	if len(names) == 0 {
		return mentionIDs
	}
	resolved, err := s.users.UserIDsByUsernames(ctx, names)
	if err != nil {
		s.logger.Warn("mention resolve", "err", err)
		return mentionIDs
	}
	for _, name := range names {
		if id, ok := resolved[name]; ok {
			mentionIDs = append(mentionIDs, id)
		}
	}
	return mentionIDs
}

func (s *Service) broadcastCreated(ctx context.Context, command Command, post *posts.Post, mentionIDs []string) []byte {
	mentionsJSON, _ := json.Marshal(mentionIDs)
	raw, _ := json.Marshal(post)
	if s.events != nil {
		data := map[string]any{
			"channel_id":   post.ChannelID,
			"channel_name": "",
			"post":         string(raw),
			"sender_name":  "",
			"team_id":      "",
			"mentions":     string(mentionsJSON),
		}
		if command.Source == SourceIncomingWebhook {
			data["sender_name"] = command.SenderName
			data["from_webhook"] = "true"
		}
		s.events.Broadcast(ws.Event{
			Event:     "posted",
			Data:      data,
			Broadcast: ws.Broadcast{ChannelID: post.ChannelID},
		})
	}

	mentionedSet := make(map[string]struct{}, len(mentionIDs))
	for _, mentionedID := range mentionIDs {
		mentionedSet[mentionedID] = struct{}{}
		if s.events != nil {
			s.events.Broadcast(ws.Event{
				Event: "mention",
				Data: map[string]any{
					"post_id":    post.ID,
					"channel_id": post.ChannelID,
					"sender_id":  post.UserID,
				},
				Broadcast: ws.Broadcast{UserID: mentionedID},
			})
		}
	}

	counters, err := s.channels.BumpUnread(ctx, post.ChannelID, post.UserID, mentionIDs)
	if err != nil {
		s.logger.Warn("bump unread", "channel", post.ChannelID, "err", err)
	}
	if s.events == nil {
		return raw
	}
	for _, counter := range counters {
		_, isMention := mentionedSet[counter.UserID]
		s.events.Broadcast(ws.Event{
			Event: "unread_updated",
			Data: map[string]any{
				"channel_id":    post.ChannelID,
				"msg_count":     counter.MsgCount,
				"mention_count": counter.MentionCount,
				"is_mention":    isMention,
				"desktop":       counter.Desktop,
			},
			Broadcast: ws.Broadcast{UserID: counter.UserID},
		})
	}
	return raw
}

func (s *Service) notifyPlugins(raw []byte) {
	if s.plugins == nil {
		return
	}
	go s.plugins.MessageHasBeenPosted(context.Background(), raw)
}

func (s *Service) dispatchOutgoing(ctx context.Context, post *posts.Post) {
	if s.outgoing == nil {
		return
	}
	authorUsername := ""
	if s.users != nil {
		if author, err := s.users.UserByID(ctx, post.UserID); err == nil && author != nil {
			authorUsername = author.Username
		}
	}
	botCaller := false
	if s.bots != nil {
		botCaller, _ = s.bots.IsBot(ctx, post.UserID)
	}
	if !botCaller {
		s.outgoing.Dispatch(context.Background(), post, authorUsername)
	}
}

func (s *Service) fetchLinkPreviews(post *posts.Post) {
	if s.linkPreviews == nil {
		return
	}
	urls := links.Extract(post.Message)
	if len(urls) == 0 {
		return
	}
	postID := post.ID
	channelID := post.ChannelID
	go func(urls []string, postID, channelID string) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.Warn("link preview panic", "post", postID, "err", recovered)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		previews := make([]posts.LinkPreview, 0, len(urls))
		for _, rawURL := range urls {
			previews = append(previews, s.linkPreviews.Fetch(ctx, rawURL))
		}
		if err := s.posts.UpdateLinkMetadata(ctx, postID, previews); err != nil {
			s.logger.Warn("link preview update", "post", postID, "err", err)
			return
		}
		patched, err := s.posts.Get(ctx, postID)
		if err != nil || patched == nil {
			return
		}
		raw, _ := json.Marshal(patched)
		if s.events != nil {
			s.events.Broadcast(ws.Event{
				Event: "post_edited",
				Data: map[string]any{
					"post":       string(raw),
					"channel_id": channelID,
				},
				Broadcast: ws.Broadcast{ChannelID: channelID},
			})
		}
	}(urls, postID, channelID)
}

func stripReservedProps(props map[string]any) map[string]any {
	if len(props) == 0 {
		return props
	}
	needsCopy := false
	for key := range props {
		if isReservedProp(key) {
			needsCopy = true
			break
		}
	}
	if !needsCopy {
		return props
	}
	filtered := make(map[string]any, len(props))
	for key, value := range props {
		if !isReservedProp(key) {
			filtered[key] = value
		}
	}
	return filtered
}

func isReservedProp(key string) bool {
	return key == "approval_request_id" || key == "scheduled_post_id" ||
		key == "from_mcp" || key == "from_webhook" || key == "webhook_depth" ||
		key == "override_username" || key == "override_icon_url" ||
		key == "from_me_command" || strings.HasPrefix(key, "_moyro_")
}

func applyServerMetadata(command Command, props map[string]any) map[string]any {
	approvalRequestID := strings.TrimSpace(command.ApprovalRequestID)
	hasMetadata := command.Source == SourceMCP || command.Source == SourceIncomingWebhook ||
		(command.Source == SourceSlashCommand && strings.EqualFold(strings.TrimSpace(command.SlashCommand), "me")) ||
		approvalRequestID != ""
	if !hasMetadata {
		return props
	}
	trusted := make(map[string]any, len(props)+4)
	for key, value := range props {
		trusted[key] = value
	}
	if command.Source == SourceMCP {
		trusted["from_mcp"] = true
	}
	if command.Source == SourceIncomingWebhook {
		trusted["from_webhook"] = "true"
		if command.WebhookDepth > 0 {
			trusted["webhook_depth"] = command.WebhookDepth
		}
		if command.OverrideUsername != "" {
			trusted["override_username"] = command.OverrideUsername
		}
		if command.OverrideIconURL != "" {
			trusted["override_icon_url"] = command.OverrideIconURL
		}
	}
	if command.Source == SourceSlashCommand && strings.EqualFold(strings.TrimSpace(command.SlashCommand), "me") {
		trusted["from_me_command"] = true
	}
	if approvalRequestID != "" {
		trusted["approval_request_id"] = approvalRequestID
	}
	return trusted
}
