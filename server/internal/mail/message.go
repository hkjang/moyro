package mail

import (
	"fmt"
	"html"
	"strings"

	"github.com/hkjang/moyro/server/internal/activityevents"
)

// FromActivity turns an inbox event into a mail notification, or reports
// that the event kind is not one people wait on. Only the events whose
// absence costs somebody something are mailed: a reviewer who does not know
// a request is waiting blocks the requester, a requester who does not learn
// the decision keeps refreshing, and an assignee who does not know a task is
// theirs lets it sit. Mentions, replies, and reminders stay in the inbox.
func FromActivity(event activityevents.Event) (Notification, bool) {
	notification := Notification{
		RecipientID: event.UserID, ActorID: event.ActorID,
		ResourceType: event.ResourceType, ResourceID: event.ResourceID,
	}
	switch event.Type {
	case activityevents.TypeApprovalRequested:
		// The requester's own "received" row is not a request to act on.
		if event.ResourceType != "approval_review" {
			return Notification{}, false
		}
		notification.Event = EventApprovalRequested
		notification.Subject = "[moyro] 검토할 승인 요청: " + summaryOr(event.Summary, event.Title)
		notification.Lines = []string{event.Title, event.Summary, "", "요청은 검토가 끝날 때까지 실행되지 않습니다. 승인 센터에서 처리해 주세요."}
		notification.Link = "/approvals/review"
	case activityevents.TypeDecided:
		notification.Event = EventApprovalDecided
		notification.Subject = "[moyro] " + event.Title
		notification.Lines = []string{event.Title, event.Summary}
		notification.Link = "/approvals/mine"
	case activityevents.TypeTaskAssigned:
		notification.Event = EventTaskAssigned
		notification.Subject = "[moyro] 새 작업이 할당되었습니다: " + summaryOr(event.Summary, event.Title)
		notification.Lines = []string{event.Title, event.Summary}
		notification.Link = "/my-work/tasks"
	default:
		return Notification{}, false
	}
	return notification, true
}

// TestNotification proves the relay works from the settings screen.
func TestNotification() Notification {
	return Notification{
		Event: EventTest, Subject: "[moyro] SMTP 발송 테스트",
		Lines: []string{"moyro 관리 화면에서 보낸 테스트 메일입니다.", "이 메일을 받았다면 SMTP 릴레이 설정이 정상입니다."},
	}
}

func summaryOr(summary, fallback string) string {
	if strings.TrimSpace(summary) != "" {
		return strings.TrimSpace(summary)
	}
	return strings.TrimSpace(fallback)
}

// compose renders one message for one recipient. Several notifications are
// listed in a single body under a combined subject so one action never
// produces a burst. It returns the first notification so the delivery log
// can name the event that started the bundle.
func compose(config Config, baseURL string, items []Notification) (Message, Notification) {
	first := items[0]
	var text, htmlBody strings.Builder
	subject := first.Subject
	if len(items) > 1 {
		subject = fmt.Sprintf("[moyro] 새 알림 %d건", len(items))
		text.WriteString(fmt.Sprintf("새 알림 %d건이 도착했습니다.\n\n", len(items)))
		htmlBody.WriteString(fmt.Sprintf("<p>새 알림 %d건이 도착했습니다.</p>", len(items)))
	}
	for index, item := range items {
		if len(items) > 1 {
			text.WriteString(fmt.Sprintf("%d. %s\n", index+1, strings.TrimPrefix(item.Subject, "[moyro] ")))
			htmlBody.WriteString("<p><strong>" + html.EscapeString(fmt.Sprintf("%d. %s", index+1, strings.TrimPrefix(item.Subject, "[moyro] "))) + "</strong><br>")
		} else {
			htmlBody.WriteString("<p>")
		}
		lines := nonEmpty(item.Lines)
		for _, line := range lines {
			text.WriteString(line + "\n")
			htmlBody.WriteString(html.EscapeString(line) + "<br>")
		}
		if link := absoluteLink(baseURL, item.Link); link != "" {
			text.WriteString("바로 열기: " + link + "\n")
			htmlBody.WriteString(`바로 열기: <a href="` + html.EscapeString(link) + `">` + html.EscapeString(link) + "</a>")
		}
		text.WriteString("\n")
		htmlBody.WriteString("</p>")
	}
	footer := "이 메일은 moyro 의 알림 설정에 따라 자동으로 발송되었습니다. 개인 설정 → 알림에서 끌 수 있습니다."
	text.WriteString("—\n" + footer + "\n")
	htmlBody.WriteString(`<p style="color:#6b7280;font-size:12px">` + html.EscapeString(footer) + "</p>")
	return Message{Subject: subject, Text: text.String(), HTML: htmlBody.String()}, first
}

func nonEmpty(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return out
}

func absoluteLink(baseURL, link string) string {
	if link == "" {
		return ""
	}
	if strings.HasPrefix(link, "http://") || strings.HasPrefix(link, "https://") {
		return link
	}
	if baseURL == "" {
		return ""
	}
	return baseURL + "/" + strings.TrimLeft(link, "/")
}
