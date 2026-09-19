package mail

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hkjang/moyro/server/internal/activityevents"
)

type fakeDirectory struct {
	addresses map[string]string
	err       error
}

func (d fakeDirectory) LookupEmails(_ context.Context, ids []string) (map[string]string, error) {
	if d.err != nil {
		return nil, d.err
	}
	out := map[string]string{}
	for _, id := range ids {
		if address, ok := d.addresses[id]; ok {
			out[id] = address
		}
	}
	return out, nil
}

type recordingSender struct {
	mu       sync.Mutex
	messages []Message
	fail     error
}

func (r *recordingSender) send(_ context.Context, _ Config, message Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages = append(r.messages, message)
	return r.fail
}

func (r *recordingSender) sent() []Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Message(nil), r.messages...)
}

func enabledConfig() Config {
	config := Default()
	config.Enabled = true
	config.SMTPHost = "relay.corp.example"
	config.FromAddress = "moyro@corp.example"
	config.BaseURL = "https://moyro.corp.example"
	return config
}

func newTestService(t *testing.T, config Config) (*Service, *recordingSender) {
	t.Helper()
	sender := &recordingSender{}
	service := New(nil, fakeDirectory{addresses: map[string]string{
		"reviewer": "reviewer@corp.example", "assignee": "assignee@corp.example",
	}}, nil)
	service.SetSender(sender.send)
	service.SetBundleWindow(10 * time.Millisecond)
	service.Configure(config)
	return service, sender
}

func reviewNotification(actor string) Notification {
	return Notification{
		Event: EventApprovalRequested, RecipientID: "reviewer", ActorID: actor,
		Subject: "[moyro] 검토할 승인 요청: 운영 공지", Lines: []string{"검토할 승인 요청: 운영 공지"}, Link: "/approvals/review",
	}
}

func TestNotifySendsNothingWhileDisabled(t *testing.T) {
	service, sender := newTestService(t, Default())
	service.Notify(context.Background(), reviewNotification("requester"))
	service.Wait()
	if len(sender.sent()) != 0 {
		t.Fatalf("disabled service sent %d messages", len(sender.sent()))
	}
	if err := service.SendTest(context.Background(), "admin@corp.example", "admin"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("SendTest while disabled = %v, want ErrDisabled", err)
	}
}

func TestNotifySendsNothingWhenIncomplete(t *testing.T) {
	config := enabledConfig()
	config.SMTPHost = ""
	service, sender := newTestService(t, config)
	service.Notify(context.Background(), reviewNotification("requester"))
	service.Wait()
	if len(sender.sent()) != 0 {
		t.Fatalf("incomplete configuration sent %d messages", len(sender.sent()))
	}
	if err := service.SendTest(context.Background(), "admin@corp.example", "admin"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("SendTest with no host = %v, want ErrInvalid", err)
	}
}

func TestNotifyDeliversInBackgroundAndSurvivesDeadRelay(t *testing.T) {
	service, sender := newTestService(t, enabledConfig())
	sender.fail = errors.New("dial tcp: connection refused")
	started := time.Now()
	service.Notify(context.Background(), reviewNotification("requester"))
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("Notify blocked for %s", elapsed)
	}
	service.Wait()
	if got := len(sender.sent()); got != 2 {
		t.Fatalf("dead relay attempts = %d, want 2 (one retry)", got)
	}
}

func TestNotifySkipsTheActorAndUnknownRecipients(t *testing.T) {
	service, sender := newTestService(t, enabledConfig())
	service.Notify(context.Background(), reviewNotification("reviewer"))
	service.Notify(context.Background(), Notification{Event: EventTaskAssigned, RecipientID: "nobody", ActorID: "x", Subject: "s"})
	service.Wait()
	if len(sender.sent()) != 0 {
		t.Fatalf("self action or unknown recipient produced %d messages", len(sender.sent()))
	}
}

func TestEventSwitchesSilenceOnlyTheirKind(t *testing.T) {
	config := enabledConfig()
	config.NotifyApprovalRequested = false
	service, sender := newTestService(t, config)
	service.Notify(context.Background(), reviewNotification("requester"))
	service.Notify(context.Background(), Notification{
		Event: EventTaskAssigned, RecipientID: "assignee", ActorID: "lead",
		Subject: "[moyro] 새 작업이 할당되었습니다: 배포 점검", Lines: []string{"새 작업이 할당되었습니다"}, Link: "/my-work/tasks",
	})
	service.Wait()
	messages := sender.sent()
	if len(messages) != 1 || messages[0].To != "assignee@corp.example" {
		t.Fatalf("messages = %#v, want only the task assignment", messages)
	}
	if !strings.Contains(messages[0].Text, "https://moyro.corp.example/my-work/tasks") {
		t.Fatalf("body lacks the absolute link:\n%s", messages[0].Text)
	}
}

func TestNotificationsForOneRecipientAreBundled(t *testing.T) {
	service, sender := newTestService(t, enabledConfig())
	service.SetBundleWindow(50 * time.Millisecond)
	for _, title := range []string{"배포 점검", "회의록 정리", "장애 보고"} {
		service.Notify(context.Background(), Notification{
			Event: EventTaskAssigned, RecipientID: "assignee", ActorID: "lead",
			Subject: "[moyro] 새 작업이 할당되었습니다: " + title, Lines: []string{"새 작업이 할당되었습니다", title},
		})
	}
	service.Wait()
	messages := sender.sent()
	if len(messages) != 1 {
		t.Fatalf("bundle produced %d messages, want 1", len(messages))
	}
	if messages[0].Subject != "[moyro] 새 알림 3건" {
		t.Fatalf("subject = %q", messages[0].Subject)
	}
	for _, title := range []string{"1. 새 작업이 할당되었습니다: 배포 점검", "2. 새 작업이 할당되었습니다: 회의록 정리", "3. 새 작업이 할당되었습니다: 장애 보고"} {
		if !strings.Contains(messages[0].Text, title) {
			t.Fatalf("body lacks %q:\n%s", title, messages[0].Text)
		}
	}
	if strings.Contains(messages[0].HTML, "<script") || !strings.Contains(messages[0].HTML, "장애 보고") {
		t.Fatalf("html body = %q", messages[0].HTML)
	}
}

func TestSendTestReportsTheRelayOutcome(t *testing.T) {
	service, sender := newTestService(t, enabledConfig())
	if err := service.SendTest(context.Background(), "admin@corp.example", "admin"); err != nil {
		t.Fatalf("SendTest = %v", err)
	}
	sender.fail = errors.New("550 relay access denied")
	err := service.SendTest(context.Background(), "admin@corp.example", "admin")
	if err == nil || !strings.Contains(err.Error(), "550") {
		t.Fatalf("SendTest against refusing relay = %v", err)
	}
	messages := sender.sent()
	if len(messages) != 2 || messages[0].Subject != "[moyro] SMTP 발송 테스트" || messages[0].To != "admin@corp.example" {
		t.Fatalf("messages = %#v", messages)
	}
}

func TestFromActivityPicksOnlyEventsPeopleWaitOn(t *testing.T) {
	cases := []struct {
		event activityevents.Event
		want  string
	}{
		{activityevents.Event{Type: activityevents.TypeApprovalRequested, ResourceType: "approval_review", UserID: "r", ActorID: "q", Title: "검토할 승인 요청: 운영 공지", Summary: "운영 공지"}, EventApprovalRequested},
		{activityevents.Event{Type: activityevents.TypeApprovalRequested, ResourceType: "approval", UserID: "q", ActorID: "q", Title: "승인 요청이 접수되었습니다"}, ""},
		{activityevents.Event{Type: activityevents.TypeDecided, ResourceType: "approval", UserID: "q", ActorID: "r", Title: "승인 요청이 승인되었습니다"}, EventApprovalDecided},
		{activityevents.Event{Type: activityevents.TypeTaskAssigned, ResourceType: "work_item", UserID: "a", ActorID: "l", Title: "새 작업이 할당되었습니다", Summary: "배포 점검"}, EventTaskAssigned},
		{activityevents.Event{Type: activityevents.TypeMention, UserID: "a", ActorID: "b", Title: "멘션"}, ""},
		{activityevents.Event{Type: activityevents.TypeReminderFired, UserID: "a", Title: "리마인더"}, ""},
	}
	for _, tc := range cases {
		notification, ok := FromActivity(tc.event)
		if tc.want == "" {
			if ok {
				t.Fatalf("%s/%s produced a mail notification %#v", tc.event.Type, tc.event.ResourceType, notification)
			}
			continue
		}
		if !ok || notification.Event != tc.want || notification.RecipientID != tc.event.UserID || notification.ActorID != tc.event.ActorID {
			t.Fatalf("%s → %#v, %v; want event %s", tc.event.Type, notification, ok, tc.want)
		}
		if notification.Subject == "" || notification.Link == "" {
			t.Fatalf("%s notification lacks subject or link: %#v", tc.want, notification)
		}
	}
}

func TestConfigValidation(t *testing.T) {
	config := Default()
	if config.Enabled || config.SMTPPort != 25 || config.Security != "auto" || config.TimeoutSeconds != 10 {
		t.Fatalf("defaults = %#v", config)
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("default configuration must validate: %v", err)
	}
	config.Enabled = true
	if err := config.Validate(); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "smtp_host") {
		t.Fatalf("enabled without host = %v", err)
	}
	config.SMTPHost = "relay.corp.example"
	if err := config.Validate(); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "from_address") {
		t.Fatalf("enabled without sender = %v", err)
	}
	config.FromAddress = "moyro@corp.example"
	if err := config.Validate(); err != nil {
		t.Fatalf("complete configuration = %v", err)
	}
	config.Security = "ssl"
	if err := config.Validate(); err == nil {
		t.Fatal("unknown security word accepted")
	}
	config.Security = "TLS"
	config.Normalize()
	if config.Security != "tls" || config.tlsMode() != "implicit" {
		t.Fatalf("security normalisation = %q/%q", config.Security, config.tlsMode())
	}
	config.FromAddress = "Ops <moyro@corp.example>"
	if err := config.Validate(); err == nil {
		t.Fatal("display name inside from_address accepted")
	}
	config.FromAddress = "moyro@corp.example"
	config.FromName = "moyro 알림"
	if got := config.From(); !strings.Contains(got, "moyro@corp.example") || !strings.Contains(got, "=?utf-8?") {
		t.Fatalf("From() = %q, want an encoded display name", got)
	}
}
