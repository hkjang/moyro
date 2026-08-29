// Package email provides SMTP-based mail delivery. The Sender interface keeps
// callers independent of transport details and makes delivery testable.
package email

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

const defaultSMTPTimeout = 10 * time.Second

// TLSMode controls how an SMTP connection is protected. The zero value keeps
// compatibility with the legacy UseTLS field; new callers should set one of
// these values explicitly once configuration exposes the choice.
type TLSMode string

const (
	TLSModeNone     TLSMode = "none"
	TLSModePlain    TLSMode = "plain" // Alias accepted for operator-facing settings.
	TLSModeSTARTTLS TLSMode = "starttls"
	TLSModeImplicit TLSMode = "implicit"
)

// ErrSTARTTLSUnavailable means encryption was required but the SMTP server
// did not advertise the STARTTLS extension. The sender never downgrades to a
// plaintext connection in this case.
var ErrSTARTTLSUnavailable = errors.New("smtp: STARTTLS is required but unavailable")

// Sender abstracts the transport. Implementations honor cancellation and
// deadlines for the whole delivery, including protocol reads and writes.
type Sender interface {
	Send(ctx context.Context, to, subject, htmlBody, textBody string) error
}

// NoopSender swallows all sends and returns nil. It is intended for tests only;
// production composition skips email workers when SMTP is unavailable so an
// undelivered message is never recorded as successfully sent.
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

// SMTPSender delivers mail using plaintext SMTP, mandatory STARTTLS, or
// implicit TLS. Host, Port, Username, Password, From, UseTLS, and Logger are
// retained for configuration compatibility. When TLSMode is empty, UseTLS is
// interpreted deterministically: port 465 uses implicit TLS and every other
// port uses STARTTLS. UseTLS=false uses plaintext SMTP.
type SMTPSender struct {
	Host, Port string
	Username   string
	Password   string
	From       string
	UseTLS     bool
	Logger     *slog.Logger

	// TLSMode overrides legacy UseTLS mapping when non-empty.
	TLSMode TLSMode
	// RootCAs permits trust of an administrator-provided internal CA. A nil
	// pool uses the operating system roots.
	RootCAs *x509.CertPool
	// Timeout caps the complete SMTP exchange. Zero uses ten seconds.
	Timeout time.Duration
}

// Send validates the envelope and headers, establishes the selected transport,
// and performs one SMTP transaction. A single derived context governs dialing,
// TLS negotiation, SMTP reads, and SMTP writes. Cancelling it closes the socket
// so no network operation survives after Send returns.
func (s *SMTPSender) Send(ctx context.Context, to, subject, htmlBody, textBody string) error {
	configuration, err := s.configuration()
	if err != nil {
		return err
	}
	fromAddress, toAddress, err := validateMessageHeaders(s.From, to, subject)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	timeout := s.Timeout
	if timeout <= 0 {
		timeout = defaultSMTPTimeout
	}
	operationContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	connection, err := configuration.dial(operationContext, s.RootCAs)
	if err != nil {
		return operationError(operationContext, "connect", err)
	}
	defer connection.Close()
	if deadline, ok := operationContext.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			return operationError(operationContext, "set connection deadline", err)
		}
	}
	stopCloseOnCancel := context.AfterFunc(operationContext, func() {
		_ = connection.Close()
	})
	defer stopCloseOnCancel()

	client, err := smtp.NewClient(connection, configuration.host)
	if err != nil {
		return operationError(operationContext, "read greeting", err)
	}
	defer client.Close()
	if err := client.Hello("localhost"); err != nil {
		return operationError(operationContext, "hello", err)
	}

	if configuration.mode == TLSModeSTARTTLS {
		if supported, _ := client.Extension("STARTTLS"); !supported {
			return ErrSTARTTLSUnavailable
		}
		if err := client.StartTLS(configuration.tlsConfig(s.RootCAs)); err != nil {
			return operationError(operationContext, "STARTTLS", err)
		}
	}

	if s.Username != "" {
		if supported, _ := client.Extension("AUTH"); !supported {
			return errors.New("smtp: server doesn't support AUTH")
		}
		auth := smtp.PlainAuth("", s.Username, s.Password, configuration.host)
		if err := client.Auth(auth); err != nil {
			return operationError(operationContext, "authenticate", err)
		}
	}
	if err := client.Mail(fromAddress.Address); err != nil {
		return operationError(operationContext, "MAIL FROM", err)
	}
	if err := client.Rcpt(toAddress.Address); err != nil {
		return operationError(operationContext, "RCPT TO", err)
	}
	dataWriter, err := client.Data()
	if err != nil {
		return operationError(operationContext, "DATA", err)
	}
	message := composeMultipart(headerAddress(fromAddress), headerAddress(toAddress), subject, htmlBody, textBody)
	if _, err := io.WriteString(dataWriter, message); err != nil {
		_ = dataWriter.Close()
		return operationError(operationContext, "write message", err)
	}
	if err := dataWriter.Close(); err != nil {
		return operationError(operationContext, "finish message", err)
	}
	if err := client.Quit(); err != nil {
		return operationError(operationContext, "quit", err)
	}
	return nil
}

type smtpConfiguration struct {
	host string
	port string
	addr string
	mode TLSMode
}

func (s *SMTPSender) configuration() (smtpConfiguration, error) {
	if s == nil {
		return smtpConfiguration{}, errors.New("smtp not configured")
	}
	host := strings.TrimSpace(s.Host)
	port := strings.TrimSpace(s.Port)
	if host == "" || port == "" {
		return smtpConfiguration{}, errors.New("smtp not configured")
	}
	if strings.ContainsAny(host, "\r\n") || strings.ContainsAny(port, "\r\n") {
		return smtpConfiguration{}, errors.New("smtp host or port contains a line break")
	}
	mode, err := s.effectiveTLSMode(port)
	if err != nil {
		return smtpConfiguration{}, err
	}
	return smtpConfiguration{
		host: host,
		port: port,
		addr: net.JoinHostPort(host, port),
		mode: mode,
	}, nil
}

func (s *SMTPSender) effectiveTLSMode(port string) (TLSMode, error) {
	configured := strings.ToLower(strings.TrimSpace(string(s.TLSMode)))
	if configured == "" {
		if !s.UseTLS {
			return TLSModeNone, nil
		}
		if port == "465" {
			return TLSModeImplicit, nil
		}
		return TLSModeSTARTTLS, nil
	}
	switch configured {
	case string(TLSModeNone), string(TLSModePlain):
		return TLSModeNone, nil
	case string(TLSModeSTARTTLS), "start_tls":
		return TLSModeSTARTTLS, nil
	case string(TLSModeImplicit), "implicit_tls", "smtps", "tls":
		return TLSModeImplicit, nil
	default:
		return "", fmt.Errorf("smtp: unsupported TLS mode %q", s.TLSMode)
	}
}

func (c smtpConfiguration) dial(ctx context.Context, roots *x509.CertPool) (net.Conn, error) {
	netDialer := &net.Dialer{}
	if c.mode != TLSModeImplicit {
		return netDialer.DialContext(ctx, "tcp", c.addr)
	}
	tlsDialer := &tls.Dialer{
		NetDialer: netDialer,
		Config:    c.tlsConfig(roots),
	}
	return tlsDialer.DialContext(ctx, "tcp", c.addr)
}

func (c smtpConfiguration) tlsConfig(roots *x509.CertPool) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: c.host,
		RootCAs:    roots,
	}
}

func validateMessageHeaders(from, to, subject string) (*mail.Address, *mail.Address, error) {
	for _, header := range []struct {
		name  string
		value string
	}{
		{name: "from", value: from},
		{name: "to", value: to},
		{name: "subject", value: subject},
	} {
		if strings.ContainsAny(header.value, "\r\n") {
			return nil, nil, fmt.Errorf("smtp: %s header contains a line break", header.name)
		}
	}
	fromAddress, err := mail.ParseAddress(strings.TrimSpace(from))
	if err != nil {
		return nil, nil, fmt.Errorf("smtp: invalid sender address: %w", err)
	}
	toAddress, err := mail.ParseAddress(strings.TrimSpace(to))
	if err != nil {
		return nil, nil, fmt.Errorf("smtp: invalid recipient address: %w", err)
	}
	return fromAddress, toAddress, nil
}

func headerAddress(address *mail.Address) string {
	if address.Name == "" {
		return address.Address
	}
	return address.String()
}

func operationError(ctx context.Context, operation string, err error) error {
	if contextError := ctx.Err(); contextError != nil {
		return fmt.Errorf("smtp %s: %w", operation, contextError)
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return fmt.Errorf("smtp %s: %w", operation, context.DeadlineExceeded)
	}
	return fmt.Errorf("smtp %s: %w", operation, err)
}

// composeMultipart builds a basic RFC 5322 message with
// multipart/alternative for text+html clients. Uses CRLF line endings as
// required by the SMTP protocol. Header values are validated before this
// helper is called.
func composeMultipart(from, to, subject, htmlBody, textBody string) string {
	boundary := fmt.Sprintf("moyro-%d", time.Now().UnixNano())
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
