// Package mail sends event notifications through a company SMTP relay.
//
// Internal relays commonly accept mail on port 25 with no credentials and no
// TLS, so those are the defaults and authentication and encryption are
// optional. Nothing here blocks a request: sending happens in the background,
// every attempt is recorded so an administrator can see what left the
// building, and the recipient directory is the account table this
// application already keeps.
package mail

import (
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
)

// Event names. Each one has a matching notify_<event> switch in Config so an
// administrator can silence one kind without turning mail off.
const (
	EventApprovalRequested = "approval_requested"
	EventApprovalDecided   = "approval_decided"
	EventTaskAssigned      = "task_assigned"
	EventTest              = "test"
)

const (
	DefaultPort           = 25
	DefaultSecurity       = "auto"
	DefaultTimeoutSeconds = 10
	MaxTimeoutSeconds     = 120
)

var ErrInvalid = errors.New("invalid mail configuration")

// Config is the administrator-managed relay configuration. Its JSON field
// names, prefixed with the settings section "mail", are the keys operators
// learn once and reuse across services. The password is not part of it: it
// lives in the encrypted settings row and only ever travels in memory.
type Config struct {
	Enabled        bool   `json:"enabled"`
	SMTPHost       string `json:"smtp_host"`
	SMTPPort       int    `json:"smtp_port"`
	Security       string `json:"security"`
	SkipTLSVerify  bool   `json:"skip_tls_verify"`
	Username       string `json:"username"`
	FromAddress    string `json:"from_address"`
	FromName       string `json:"from_name"`
	BaseURL        string `json:"base_url"`
	TimeoutSeconds int    `json:"timeout_seconds"`

	NotifyApprovalRequested bool `json:"notify_approval_requested"`
	NotifyApprovalDecided   bool `json:"notify_approval_decided"`
	NotifyTaskAssigned      bool `json:"notify_task_assigned"`

	// Password is filled from the secret row by the owner of the settings
	// store and is never serialized.
	Password string `json:"-"`
}

// Default is off, so a fresh installation changes nothing.
func Default() Config {
	return Config{
		SMTPPort: DefaultPort, Security: DefaultSecurity, TimeoutSeconds: DefaultTimeoutSeconds,
		FromName:                "moyro",
		NotifyApprovalRequested: true, NotifyApprovalDecided: true, NotifyTaskAssigned: true,
	}
}

// Normalize trims and lower-cases what is safe to canonicalize and fills
// blanks with defaults; Validate then judges the result.
func (c *Config) Normalize() {
	c.SMTPHost = strings.TrimSpace(c.SMTPHost)
	c.Username = strings.TrimSpace(c.Username)
	c.FromAddress = strings.TrimSpace(c.FromAddress)
	c.FromName = strings.TrimSpace(c.FromName)
	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	c.Security = strings.ToLower(strings.TrimSpace(c.Security))
	if c.Security == "" {
		c.Security = DefaultSecurity
	}
	if c.SMTPPort == 0 {
		c.SMTPPort = DefaultPort
	}
	if c.TimeoutSeconds == 0 {
		c.TimeoutSeconds = DefaultTimeoutSeconds
	}
}

// Validate rejects what a relay could never be reached with. A disabled
// configuration only has to be well-formed, so an administrator can save a
// half-filled form and come back to it.
func (c Config) Validate() error {
	switch c.Security {
	case "auto", "none", "starttls", "tls":
	default:
		return fmt.Errorf("%w: security must be auto, none, starttls, or tls", ErrInvalid)
	}
	if c.SMTPPort < 1 || c.SMTPPort > 65535 {
		return fmt.Errorf("%w: smtp_port must be between 1 and 65535", ErrInvalid)
	}
	if c.TimeoutSeconds < 1 || c.TimeoutSeconds > MaxTimeoutSeconds {
		return fmt.Errorf("%w: timeout_seconds must be between 1 and %d", ErrInvalid, MaxTimeoutSeconds)
	}
	for name, value := range map[string]string{"smtp_host": c.SMTPHost, "username": c.Username, "from_address": c.FromAddress, "from_name": c.FromName, "base_url": c.BaseURL} {
		if strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("%w: %s contains a line break", ErrInvalid, name)
		}
	}
	if len(c.SMTPHost) > 253 || strings.ContainsAny(c.SMTPHost, " /\\@:") {
		return fmt.Errorf("%w: smtp_host must be a hostname or IP address without a scheme, port, or path", ErrInvalid)
	}
	if c.FromAddress != "" {
		if _, err := mail.ParseAddress(c.FromAddress); err != nil || strings.ContainsAny(c.FromAddress, "<>\"") {
			return fmt.Errorf("%w: from_address must be a plain email address", ErrInvalid)
		}
	}
	if c.BaseURL != "" && !strings.HasPrefix(c.BaseURL, "http://") && !strings.HasPrefix(c.BaseURL, "https://") {
		return fmt.Errorf("%w: base_url must start with http:// or https://", ErrInvalid)
	}
	if !c.Enabled {
		return nil
	}
	return c.validateForSending()
}

// validateForSending is what has to hold before a single message can go out.
func (c Config) validateForSending() error {
	if c.SMTPHost == "" {
		return fmt.Errorf("%w: smtp_host is required", ErrInvalid)
	}
	if c.FromAddress == "" {
		return fmt.Errorf("%w: from_address is required", ErrInvalid)
	}
	return nil
}

// Allows reports whether an event kind is switched on. Unknown events are
// sent, so adding a notification never requires a settings change first.
func (c Config) Allows(event string) bool {
	switch event {
	case EventApprovalRequested:
		return c.NotifyApprovalRequested
	case EventApprovalDecided:
		return c.NotifyApprovalDecided
	case EventTaskAssigned:
		return c.NotifyTaskAssigned
	}
	return true
}

// From is the RFC 5322 From header value.
func (c Config) From() string {
	if c.FromName == "" {
		return c.FromAddress
	}
	return (&mail.Address{Name: c.FromName, Address: c.FromAddress}).String()
}

func (c Config) timeout() time.Duration {
	if c.TimeoutSeconds <= 0 {
		return DefaultTimeoutSeconds * time.Second
	}
	return time.Duration(c.TimeoutSeconds) * time.Second
}

// tlsMode maps the operator-facing security word onto the transport's modes.
func (c Config) tlsMode() string {
	switch c.Security {
	case "tls":
		return "implicit"
	case "none", "starttls", "auto":
		return c.Security
	}
	return DefaultSecurity
}
