package approval

import (
	"encoding/json"
	"regexp"
	"strings"
)

const (
	PreviewMessageLimit  = 4_000
	previewScanLimit     = 8_192
	PreviewOmittedValue  = "[이하 생략]"
	PreviewRedactedValue = "[보호된 값]"
)

type PreviewActor struct {
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
}

type PreviewTarget struct {
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
}

type PreviewChange struct {
	Label string `json:"label"`
	After string `json:"after"`
}

type PreviewPolicy struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// RequestPreview is an allowlisted, display-oriented projection of a durable
// approval payload. It deliberately exposes only the message for supported
// actions, after server-side redaction and truncation.
type RequestPreview struct {
	Title           string          `json:"title"`
	RiskLevel       string          `json:"risk_level"`
	Actor           PreviewActor    `json:"actor"`
	Target          PreviewTarget   `json:"target"`
	Changes         []PreviewChange `json:"changes"`
	Policy          PreviewPolicy   `json:"policy"`
	SecretsRedacted bool            `json:"secrets_redacted"`
}

// RequestView is the only approval-request shape safe to return across API
// boundaries. Request.Payload remains available only to internal execution
// paths; it is never embedded in this view.
type RequestView struct {
	ID             string         `json:"id"`
	PolicyID       string         `json:"policy_id"`
	ActionType     string         `json:"action_type"`
	RequesterID    string         `json:"requester_id"`
	TeamID         string         `json:"team_id"`
	ResourceType   string         `json:"resource_type"`
	ResourceID     string         `json:"resource_id"`
	Status         string         `json:"status"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	CreateAt       int64          `json:"create_at"`
	UpdateAt       int64          `json:"update_at"`
	DecidedAt      int64          `json:"decided_at"`
	ExecutedAt     int64          `json:"executed_at"`
	ExpiresAt      int64          `json:"expires_at"`
	Preview        RequestPreview `json:"preview"`
}

type previewRedactor struct {
	expression  *regexp.Regexp
	replacement string
}

var previewRedactors = []previewRedactor{
	{regexp.MustCompile(`(?is)-----BEGIN( [A-Z0-9]+)* PRIVATE KEY-----.*?-----END( [A-Z0-9]+)* PRIVATE KEY-----`), PreviewRedactedValue},
	// Also fail closed when the visible scan window ends inside a PEM block.
	{regexp.MustCompile(`(?is)-----BEGIN( [A-Z0-9]+)* PRIVATE KEY-----.*`), PreviewRedactedValue},
	{regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)[^\s/@:]+:[^\s/@]+@`), `${1}` + PreviewRedactedValue + `@`},
	{regexp.MustCompile(`(?i)(["'])(password|passwd|pwd|secret|api[ _-]?key|apikey|access[ _-]?token|refresh[ _-]?token|client[ _-]?secret|session[ _-]?token|private[ _-]?key|signing[ _-]?key|webhook[ _-]?secret|authorization|credential|token)(["'])(\s*(=|:)\s*)(\[보호된 값\]|Bearer\s+[A-Za-z0-9._~+/=-]{8,}|Basic\s+[A-Za-z0-9+/=]{8,}|"[^"\r\n]*"|'[^'\r\n]*'|[^\s,;}\]]+)`), `${1}${2}${3}${4}` + PreviewRedactedValue},
	{regexp.MustCompile(`(?i)\b(password|passwd|pwd|secret|api[ _-]?key|apikey|access[ _-]?token|refresh[ _-]?token|client[ _-]?secret|session[ _-]?token|private[ _-]?key|signing[ _-]?key|webhook[ _-]?secret|authorization|credential|token)(\s*(=|:)\s*)(\[보호된 값\]|Bearer\s+[A-Za-z0-9._~+/=-]{8,}|Basic\s+[A-Za-z0-9+/=]{8,}|"[^"\r\n]*"|'[^'\r\n]*'|[^\s,;}\]]+)`), `${1}${2}` + PreviewRedactedValue},
	{regexp.MustCompile(`(?i)\b(Bearer|Basic)\s+[A-Za-z0-9._~+/=-]{8,}`), PreviewRedactedValue},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\b`), PreviewRedactedValue},
	{regexp.MustCompile(`(?i)\b(moyro_|mdp_|gh[pousr]_|xox[baprs]-)[A-Za-z0-9_-]{8,}\b`), PreviewRedactedValue},
	{regexp.MustCompile(`\b(glpat-|AIza)[A-Za-z0-9_-]{12,}\b`), PreviewRedactedValue},
	{regexp.MustCompile(`\b(sk|pk)-[A-Za-z0-9_-]{12,}\b`), PreviewRedactedValue},
	{regexp.MustCompile(`\bAKIA[A-Z0-9]{16}\b`), PreviewRedactedValue},
}

func RedactPreviewMessage(input string) (string, bool) {
	scanned := make([]rune, 0, min(len(input), previewScanLimit))
	originalTruncated := false
	for _, char := range input {
		if len(scanned) == previewScanLimit {
			originalTruncated = true
			break
		}
		scanned = append(scanned, char)
	}
	if len(scanned) > PreviewMessageLimit {
		originalTruncated = true
	}
	value := string(scanned)
	redacted := false
	for _, redactor := range previewRedactors {
		if redactor.expression.MatchString(value) {
			redacted = true
			value = redactor.expression.ReplaceAllString(value, redactor.replacement)
		}
	}
	if originalTruncated {
		visibleLimit := PreviewMessageLimit - len([]rune(PreviewOmittedValue)) - 1
		visible := []rune(value)
		if len(visible) > visibleLimit {
			visible = visible[:visibleLimit]
		}
		value = strings.TrimRight(string(visible), "\r\n") + "\n" + PreviewOmittedValue
	}
	return value, redacted
}

func previewTargetType(resourceType string) string {
	switch resourceType {
	case "channel", "team":
		return resourceType
	default:
		return "resource"
	}
}

func makeRequestPreview(request *Request, targetDisplayName string) RequestPreview {
	preview := RequestPreview{
		Title: "승인 요청", RiskLevel: "unknown",
		Actor:           PreviewActor{Type: "automation", DisplayName: "자동화 요청"},
		Target:          PreviewTarget{Type: previewTargetType(request.ResourceType), DisplayName: strings.TrimSpace(targetDisplayName)},
		Changes:         []PreviewChange{},
		Policy:          PreviewPolicy{Name: "관리자 승인 정책", Reason: "이 작업은 관리자 승인 정책의 보호 대상입니다."},
		SecretsRedacted: true,
	}
	if preview.Target.DisplayName == "" {
		switch preview.Target.Type {
		case "channel":
			preview.Target.DisplayName = "요청 대상 채널"
		case "team":
			preview.Target.DisplayName = "요청 대상 팀"
		default:
			preview.Target.DisplayName = "보호된 작업 대상"
		}
	}

	changeLabel := ""
	switch request.ActionType {
	case "mcp.create_post":
		preview.Title = "채널 메시지 작성"
		preview.RiskLevel = "medium"
		preview.Actor = PreviewActor{Type: "mcp_key", DisplayName: "MCP 자동화"}
		preview.Policy.Reason = "MCP 메시지 작성은 관리자 승인 정책의 보호 대상입니다."
		changeLabel = "작성할 메시지"
	case "mcp.reply_to_thread":
		preview.Title = "스레드 답글 작성"
		preview.RiskLevel = "medium"
		preview.Actor = PreviewActor{Type: "mcp_key", DisplayName: "MCP 자동화"}
		preview.Policy.Reason = "MCP 메시지 작성은 관리자 승인 정책의 보호 대상입니다."
		changeLabel = "작성할 답글"
	}
	if changeLabel != "" {
		var payload struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(request.Payload, &payload) == nil && strings.TrimSpace(payload.Message) != "" {
			message, _ := RedactPreviewMessage(payload.Message)
			preview.Changes = append(preview.Changes, PreviewChange{Label: changeLabel, After: message})
		}
	}
	return preview
}

func ToRequestView(request *Request, targetDisplayName string) *RequestView {
	if request == nil {
		return nil
	}
	return &RequestView{
		ID: request.ID, PolicyID: request.PolicyID, ActionType: request.ActionType,
		RequesterID: request.RequesterID, TeamID: request.TeamID,
		ResourceType: request.ResourceType, ResourceID: request.ResourceID,
		Status: request.Status, IdempotencyKey: request.IdempotencyKey,
		CreateAt: request.CreateAt, UpdateAt: request.UpdateAt, DecidedAt: request.DecidedAt,
		ExecutedAt: request.ExecutedAt, ExpiresAt: request.ExpiresAt,
		Preview: makeRequestPreview(request, targetDisplayName),
	}
}

func ToRequestViews(requests []Request) []RequestView {
	views := make([]RequestView, 0, len(requests))
	for i := range requests {
		views = append(views, *ToRequestView(&requests[i], ""))
	}
	return views
}
