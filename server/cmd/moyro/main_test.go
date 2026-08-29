package main

import (
	"io"
	"log/slog"
	"testing"

	"github.com/hkjang/moyro/server/internal/config"
	"github.com/hkjang/moyro/server/internal/email"
)

func TestConfiguredDigestSenderRequiresSMTPHost(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, cfg := range []*config.Config{
		nil,
		{},
		{SMTPHost: "   "},
	} {
		if sender := configuredDigestSender(cfg, logger); sender != nil {
			t.Fatalf("configuredDigestSender(%#v) = %T, want nil", cfg, sender)
		}
	}
}

func TestConfiguredDigestSenderBuildsRealSMTPTransport(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{
		SMTPHost: " smtp.internal ", SMTPPort: "465",
		SMTPUsername: "moyro", SMTPPassword: "secret",
		SMTPFrom: "noreply@moyro.internal", SMTPTLS: true,
	}

	sender := configuredDigestSender(cfg, logger)
	smtpSender, ok := sender.(*email.SMTPSender)
	if !ok {
		t.Fatalf("configuredDigestSender() = %T, want *email.SMTPSender", sender)
	}
	if smtpSender.Host != "smtp.internal" || smtpSender.Port != cfg.SMTPPort ||
		smtpSender.Username != cfg.SMTPUsername || smtpSender.Password != cfg.SMTPPassword ||
		smtpSender.From != cfg.SMTPFrom || smtpSender.UseTLS != cfg.SMTPTLS || smtpSender.Logger != logger {
		t.Fatal("SMTP sender did not preserve configuration")
	}
}
