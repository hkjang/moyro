package mail

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/hkjang/moyro/server/internal/email"
	"github.com/hkjang/moyro/server/internal/store"
)

var ErrDisabled = errors.New("mail is disabled")

// DefaultBundleWindow is how long a recipient's first notification waits for
// company before it is sent. One action that produces several notifications
// for the same person (a recurring task and its spawned copy, a batch of
// approvals) then arrives as one message instead of a burst.
const DefaultBundleWindow = 3 * time.Second

// MaxBundleItems caps how many notifications one message carries.
const MaxBundleItems = 50

const (
	StatusQueued = "queued"
	StatusSent   = "sent"
	StatusFailed = "failed"
)

// Directory resolves account ids to addresses. It is the account table this
// application already keeps; mail never grows a user list of its own.
// Accounts without an address, or that opted out of event mail, are absent
// from the result.
type Directory interface {
	LookupEmails(ctx context.Context, userIDs []string) (map[string]string, error)
}

// Notification is one event for one recipient before the address is known.
type Notification struct {
	Event        string
	RecipientID  string
	ActorID      string
	Subject      string
	Lines        []string
	Link         string
	ResourceType string
	ResourceID   string
}

// Message is what reaches the transport.
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// Delivery is one recorded attempt to reach one address. The body is not
// kept.
type Delivery struct {
	ID           string `json:"id"`
	Event        string `json:"event"`
	RecipientID  string `json:"recipient_id,omitempty"`
	Recipient    string `json:"recipient"`
	Subject      string `json:"subject"`
	ActorID      string `json:"actor_id,omitempty"`
	ResourceType string `json:"resource_type,omitempty"`
	ResourceID   string `json:"resource_id,omitempty"`
	Status       string `json:"status"`
	Attempts     int    `json:"attempts"`
	ErrorMessage string `json:"error_message,omitempty"`
	CreateAt     int64  `json:"create_at"`
	UpdateAt     int64  `json:"update_at"`
}

type Summary struct {
	Total  int            `json:"total"`
	Status map[string]int `json:"status"`
}

type Page struct {
	Items   []Delivery `json:"items"`
	Summary Summary    `json:"summary"`
}

// Sender is the transport. Deliver is the production value; tests swap it.
type Sender func(ctx context.Context, config Config, message Message) error

type Service struct {
	db        *store.DB
	directory Directory
	logger    *slog.Logger
	config    atomic.Pointer[Config]
	send      Sender
	now       func() time.Time
	window    time.Duration
	// fallbackBaseURL is consulted for links when mail.base_url is blank; it
	// is the site's public address.
	fallbackBaseURL func() string

	mu       sync.Mutex
	pending  map[string]*bundle
	inflight sync.WaitGroup
}

type bundle struct {
	items []Notification
}

// New builds a service that is off until Configure is called with an enabled
// configuration. A nil db keeps no delivery log, which only tests want.
func New(db *store.DB, directory Directory, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	service := &Service{
		db: db, directory: directory, logger: logger, send: Deliver,
		now: func() time.Time { return time.Now().UTC() }, window: DefaultBundleWindow,
		fallbackBaseURL: func() string { return "" }, pending: map[string]*bundle{},
	}
	initial := Default()
	service.config.Store(&initial)
	return service
}

// SetSender replaces the transport so tests can run without a relay.
func (s *Service) SetSender(sender Sender) { s.send = sender }

// SetBundleWindow shortens the wait in tests.
func (s *Service) SetBundleWindow(window time.Duration) { s.window = window }

// SetBaseURLFallback supplies the site address used when mail.base_url is blank.
func (s *Service) SetBaseURLFallback(fn func() string) {
	if fn != nil {
		s.fallbackBaseURL = fn
	}
}

// Configure replaces the live configuration, password included. The
// password stays in this process's memory and is never logged.
func (s *Service) Configure(config Config) {
	copied := config
	s.config.Store(&copied)
}

// Config returns the live configuration including the password. HTTP
// handlers must redact before answering.
func (s *Service) Config() Config {
	if current := s.config.Load(); current != nil {
		return *current
	}
	return Default()
}

// Enabled reports whether event mail is on and complete enough to send.
func (s *Service) Enabled() bool {
	config := s.Config()
	return config.Enabled && config.validateForSending() == nil
}

// Notify queues one notification. It returns immediately: the address lookup
// and the relay conversation happen in the background, and a dead relay is
// recorded, not surfaced to the request that caused the event. Nobody is
// told about their own action.
func (s *Service) Notify(ctx context.Context, notification Notification) {
	config := s.Config()
	if !config.Enabled || !config.Allows(notification.Event) {
		return
	}
	if err := config.validateForSending(); err != nil {
		s.logger.Warn("event mail skipped", "event", notification.Event, "reason", err)
		return
	}
	recipient := strings.TrimSpace(notification.RecipientID)
	if recipient == "" || recipient == strings.TrimSpace(notification.ActorID) {
		return
	}
	notification.RecipientID = recipient
	s.mu.Lock()
	defer s.mu.Unlock()
	current, waiting := s.pending[recipient]
	if !waiting {
		current = &bundle{}
		s.pending[recipient] = current
		s.inflight.Add(1)
		time.AfterFunc(s.window, func() { s.flush(recipient) })
	}
	if len(current.items) < MaxBundleItems {
		current.items = append(current.items, notification)
	}
}

// Wait blocks until every queued bundle has been attempted. Tests use it;
// shutdown may as well.
func (s *Service) Wait() { s.inflight.Wait() }

func (s *Service) flush(recipient string) {
	defer s.inflight.Done()
	s.mu.Lock()
	current := s.pending[recipient]
	delete(s.pending, recipient)
	s.mu.Unlock()
	if current == nil || len(current.items) == 0 {
		return
	}
	config := s.Config()
	if !config.Enabled || config.validateForSending() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*config.timeout()+20*time.Second)
	defer cancel()

	address := s.resolve(ctx, recipient)
	if address == "" {
		s.logger.Debug("event mail skipped: recipient has no address or opted out", "recipient_id", recipient)
		return
	}
	message, first := compose(config, s.baseURL(config), current.items)
	message.To = address
	delivery := Delivery{
		ID: uuid.NewString(), Event: first.Event, RecipientID: recipient, Recipient: address,
		Subject: message.Subject, ActorID: first.ActorID, ResourceType: first.ResourceType, ResourceID: first.ResourceID,
		Status: StatusQueued,
	}
	if len(current.items) > 1 {
		delivery.Event = "bundle"
	}
	s.record(ctx, &delivery)

	// One retry: a relay that briefly refuses a connection is common, and
	// losing the notification is worse than a short wait.
	var err error
	for attempt := 1; attempt <= 2; attempt++ {
		delivery.Attempts = attempt
		if err = s.send(ctx, config, message); err == nil {
			break
		}
		if attempt == 1 {
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
			}
		}
	}
	s.complete(ctx, &delivery, err)
}

// SendTest delivers one message right now and reports the outcome, which is
// what the administrator's test button needs. The attempt is recorded like
// any other.
func (s *Service) SendTest(ctx context.Context, recipient, actorID string) error {
	config := s.Config()
	if !config.Enabled {
		return ErrDisabled
	}
	if err := config.validateForSending(); err != nil {
		return err
	}
	recipient = strings.TrimSpace(recipient)
	message, _ := compose(config, s.baseURL(config), []Notification{TestNotification()})
	message.To = recipient
	delivery := Delivery{
		ID: uuid.NewString(), Event: EventTest, Recipient: recipient, Subject: message.Subject,
		ActorID: actorID, Status: StatusQueued,
	}
	s.record(ctx, &delivery)
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), config.timeout()+5*time.Second)
	defer cancel()
	delivery.Attempts = 1
	err := s.send(sendCtx, config, message)
	s.complete(sendCtx, &delivery, err)
	return err
}

func (s *Service) baseURL(config Config) string {
	if config.BaseURL != "" {
		return config.BaseURL
	}
	return strings.TrimRight(strings.TrimSpace(s.fallbackBaseURL()), "/")
}

func (s *Service) resolve(ctx context.Context, recipient string) string {
	if s.directory == nil {
		return ""
	}
	addresses, err := s.directory.LookupEmails(ctx, []string{recipient})
	if err != nil {
		s.logger.Warn("event mail recipient lookup failed", "recipient_id", recipient, "err", err)
		return ""
	}
	return strings.TrimSpace(addresses[recipient])
}

func (s *Service) record(ctx context.Context, delivery *Delivery) {
	delivery.CreateAt = s.now().UnixMilli()
	delivery.UpdateAt = delivery.CreateAt
	if s.db == nil {
		return
	}
	if _, err := s.db.Pool.Exec(ctx, `
		INSERT INTO mail_deliveries (id, event, recipient_id, recipient, subject, actor_id, resource_type, resource_id, status, attempts, create_at, update_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'queued',0,$9,$9)
	`, delivery.ID, delivery.Event, delivery.RecipientID, delivery.Recipient, truncate(delivery.Subject, 300),
		delivery.ActorID, delivery.ResourceType, delivery.ResourceID, delivery.CreateAt); err != nil {
		s.logger.Warn("mail delivery was not recorded", "err", err)
	}
}

func (s *Service) complete(ctx context.Context, delivery *Delivery, cause error) {
	delivery.Status, delivery.ErrorMessage = StatusSent, ""
	if cause != nil {
		delivery.Status, delivery.ErrorMessage = StatusFailed, truncate(cause.Error(), 1000)
		s.logger.Warn("event mail failed", "event", delivery.Event, "recipient", delivery.Recipient, "attempts", delivery.Attempts, "err", cause)
	}
	delivery.UpdateAt = s.now().UnixMilli()
	if s.db == nil {
		return
	}
	if _, err := s.db.Pool.Exec(ctx, `
		UPDATE mail_deliveries SET status=$2, attempts=GREATEST(attempts,$3), error_message=$4, update_at=$5 WHERE id=$1
	`, delivery.ID, delivery.Status, max(delivery.Attempts, 1), delivery.ErrorMessage, delivery.UpdateAt); err != nil {
		s.logger.Warn("mail delivery status was not recorded", "err", err)
	}
}

// Deliveries lists what was attempted, newest first, with a status breakdown.
func (s *Service) Deliveries(ctx context.Context, status string, limit int) (Page, error) {
	page := Page{Items: []Delivery{}, Summary: Summary{Status: map[string]int{}}}
	if s.db == nil {
		return page, nil
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}
	query := `SELECT id, event, recipient_id, recipient, subject, actor_id, resource_type, resource_id, status, attempts, error_message, create_at, update_at FROM mail_deliveries`
	args := []any{}
	if status = strings.TrimSpace(status); status != "" {
		args = append(args, status)
		query += ` WHERE status=$1`
	}
	args = append(args, limit)
	rows, err := s.db.Pool.Query(ctx, query+fmt.Sprintf(` ORDER BY create_at DESC, id LIMIT $%d`, len(args)), args...)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var item Delivery
		if err := rows.Scan(&item.ID, &item.Event, &item.RecipientID, &item.Recipient, &item.Subject, &item.ActorID,
			&item.ResourceType, &item.ResourceID, &item.Status, &item.Attempts, &item.ErrorMessage, &item.CreateAt, &item.UpdateAt); err != nil {
			return Page{}, err
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}
	counts, err := s.db.Pool.Query(ctx, `SELECT status, count(*) FROM mail_deliveries GROUP BY 1`)
	if err != nil {
		return Page{}, err
	}
	defer counts.Close()
	for counts.Next() {
		var key string
		var count int
		if err := counts.Scan(&key, &count); err != nil {
			return Page{}, err
		}
		page.Summary.Status[key] = count
		page.Summary.Total += count
	}
	return page, counts.Err()
}

// Deliver opens a relay connection and sends one message. It is the
// production Sender.
func Deliver(ctx context.Context, config Config, message Message) error {
	sender := &email.SMTPSender{
		Host: config.SMTPHost, Port: fmt.Sprint(config.SMTPPort),
		Username: config.Username, Password: config.Password,
		From: config.From(), TLSMode: email.TLSMode(config.tlsMode()),
		InsecureSkipVerify: config.SkipTLSVerify, Timeout: config.timeout(),
	}
	return sender.Send(ctx, message.To, message.Subject, message.HTML, message.Text)
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
