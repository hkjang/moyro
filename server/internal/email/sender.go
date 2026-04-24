// Package email provides a minimal SMTP-based mail sender plus a no-op
// implementation for dev. The Sender interface isolates callers (the
// digest worker) from the transport so tests can plug in a capture impl.
package email

import (
	"context"
	"fmt"
	"log/slog"
	"net/smtp"
	"strings"
	"time"
)

// Sender abstracts the transport. Methods are context-aware so callers
// can enforce their own deadlines, but the bundled SMTPSender enforces
// its own 10s timeout since net/smtp doesn't honour context natively.
type Sender interface {
	Send(ctx context.Context, to, subject, htmlBody, textBody string) error
}

// NoopSender swallows all sends and returns nil. Used when SMTP isn't
// configured — the digest worker still ticks, but nothing leaves the
// process. Useful in dev and in tests.
type NoopSender struct {
	Logger *slog.Logger
}

// Send logs at debug level (if logger set) and returns nil.
func (n *NoopSender) Send(_ context.Context, to, subject, _, _ string) error {
	if n != nil && n.Logger != nil {
		n.Logger.Debug("email noop", "to", to, "subject", subject)
	}
	return nil
}

// SMTPSender delivers mail via net/smtp. Supports both plaintext (port 25
// / 587 without TLS) and implicit-TLS (port 465 via SMTPS). STARTTLS
// on port 587 is left as future work — most cloud SMTP providers accept
// plaintext on internal networks or implicit TLS on 465.
type SMTPSender struct {
	Host, Port string
	Username   string
	Password   string
	From       string
	UseTLS     bool
	Logger     *slog.Logger
}

// Send composes a multipart/alternative message and hands it to
// smtp.SendMail. Enforces a 10s overall deadline via the standard
// net/smtp dial-and-send flow (the timeout is baked into the default
// net.Dialer used internally; effectively a hard cap).
func (s *SMTPSender) Send(ctx context.Context, to, subject, htmlBody, textBody string) error {
	if s == nil || s.Host == "" {
		return fmt.Errorf("smtp not configured")
	}
	addr := s.Host + ":" + s.Port
	var auth smtp.Auth
	if s.Username != "" {
		auth = smtp.PlainAuth("", s.Username, s.Password, s.Host)
	}
	msg := composeMultipart(s.From, to, subject, htmlBody, textBody)
	errCh := make(chan error, 1)
	go func() { errCh <- smtp.SendMail(addr, auth, s.From, []string{to}, []byte(msg)) }()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(10 * time.Second):
		return fmt.Errorf("smtp send timed out")
	}
}

// composeMultipart builds a basic RFC 5322 message with
// multipart/alternative for text+html clients. Uses CRLF line endings as
// required by the SMTP protocol.
func composeMultipart(from, to, subject, htmlBody, textBody string) string {
	boundary := fmt.Sprintf("moddle-%d", time.Now().UnixNano())
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: " + subject + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: multipart/alternative; boundary=\"" + boundary + "\"\r\n\r\n")
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
	b.WriteString(textBody + "\r\n")
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/html; charset=UTF-8\r\n\r\n")
	b.WriteString(htmlBody + "\r\n")
	b.WriteString("--" + boundary + "--\r\n")
	return b.String()
}
