package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hkjang/moyro/server/internal/bootstrap"
	"github.com/hkjang/moyro/server/internal/channels"
	"github.com/hkjang/moyro/server/internal/config"
	"github.com/hkjang/moyro/server/internal/digest"
	"github.com/hkjang/moyro/server/internal/email"
	"github.com/hkjang/moyro/server/internal/httpapi"
	"github.com/hkjang/moyro/server/internal/metrics"
	"github.com/hkjang/moyro/server/internal/pluginhost"
	"github.com/hkjang/moyro/server/internal/secrets"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/hkjang/moyro/server/internal/teams"
	"github.com/hkjang/moyro/server/internal/ws"
	"github.com/hkjang/moyro/server/internal/ws/redisfanout"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("config load", "err", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	db, err := store.Open(ctx, cfg.PostgresDSN)
	if err != nil {
		logger.Error("db open", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := store.Migrate(ctx, db); err != nil {
		logger.Error("migrate", "err", err)
		os.Exit(1)
	}
	bootstrapService, err := bootstrap.New(db)
	if err != nil {
		logger.Error("bootstrap init", "err", err)
		os.Exit(1)
	}
	bootstrapResult, err := bootstrapService.EnsureAdmin(ctx, cfg.BootstrapAdminEmail, cfg.BootstrapAdminPassword)
	if err != nil {
		logger.Error("bootstrap administrator", "err", err)
		os.Exit(1)
	}
	logger.Info("bootstrap administrator ready", "user_id", bootstrapResult.AdminUserID, "created", bootstrapResult.Created, "already_complete", bootstrapResult.AlreadyComplete)

	// One-shot bootstrap: ensure default team/channel exist and backfill
	// existing users so they have somewhere to chat immediately after login.
	if err := bootstrapDefaults(ctx, db); err != nil {
		logger.Warn("bootstrap defaults", "err", err)
	}

	hub := ws.NewHub()
	go hub.Run(ctx)

	// Sample hub and pool state onto the prometheus collectors on a slow
	// tick. 5s is fine — these are for trend graphs, not per-request
	// accuracy — and it keeps the hub's hot path free of metrics coupling.
	//
	// The hub's drop counters matter most here: shedding events is how it
	// stays responsive under load, and without this loop that would be
	// invisible. Pool saturation is sampled from the same tick because
	// exhaustion and event shedding usually have the same root cause.
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				stats := hub.Stats()
				metrics.ObserveWSClients(stats.Clients)
				metrics.ObserveWSUsers(stats.Users)
				metrics.ObserveWSDrops(stats.DroppedEvents, stats.DroppedSends, stats.AudienceFailures)
				metrics.ObserveDBPool(db.Pool.Stat())
			}
		}
	}()

	// Phase 17: optional Redis pub/sub fan-out across server instances.
	// Empty RedisURL skips wiring entirely (dev / single-instance stays
	// bit-identical). When dialing fails we log a warning and proceed
	// single-instance — Redis must never be load-bearing for startup.
	if cfg.RedisURL != "" {
		fo, ferr := redisfanout.Dial(ctx, cfg.RedisURL, logger)
		if ferr != nil {
			logger.Warn("redis fanout disabled", "err", ferr)
		} else {
			hub.SetPublisher(fo)
			fo.Subscribe(ctx, hub.InjectEvent)
			defer fo.Close()
			logger.Info("redis fanout active", "origin", fo.Origin())
		}
	}

	pluginSecrets, err := secrets.New(cfg.EncryptionKey)
	if err != nil {
		logger.Error("plugin secret manager", "err", err)
		os.Exit(1)
	}
	host, err := pluginhost.NewWithRuntime(cfg.PluginDir, db, pluginSecrets, logger)
	if err != nil {
		logger.Error("plugin runtime", "err", err)
		os.Exit(1)
	}
	// Build the application services before activating persisted plugins.
	// Official Mattermost plugins routinely create bots, register commands, or
	// schedule work from OnActivate; their API must already be bound to the
	// shared post/file/event/audit pipeline at that point.
	backend := httpapi.New(cfg, db, hub, host, logger)
	if err := host.LoadAll(ctx); err != nil {
		host.Shutdown()
		logger.Error("plugin recovery", "err", err)
		os.Exit(1)
	}

	// Email digests are meaningful only when a real SMTP transport exists.
	// Skipping the worker entirely prevents a disabled deployment from
	// recording last_digest_at for messages that were never delivered.
	if mailSender := configuredDigestSender(cfg, logger); mailSender != nil {
		digestWorker := digest.NewWorker(db, mailSender, logger, cfg.PublicBaseURL)
		go digestWorker.Run(ctx)
		logger.Info("email digest worker active", "smtp_host", cfg.SMTPHost)
	} else {
		logger.Info("email digest worker disabled", "reason", "smtp_not_configured")
	}

	// Phase 19: build the router plus the scheduled-posts + reminders
	// background workers in one shot. Workers need services the router
	// already owns (posts, files) — keeping construction in one place
	// avoids duplicating the fs-vs-s3 switch.
	go backend.Scheduled.Run(ctx)
	go backend.Reminders.Run(ctx)
	go backend.Approvals.Run(ctx)
	go backend.Automations.Run(ctx)

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           backend.Router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("moyro listening", "addr", cfg.Listen)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("serve", "err", err)
			cancel()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	_ = srv.Shutdown(shutdownCtx)
	host.Shutdown()
}

// configuredDigestSender returns nil when SMTP is unavailable. Production
// composition must treat nil as "do not start the digest worker"; substituting
// a successful no-op sender would make undelivered digests look delivered.
func configuredDigestSender(cfg *config.Config, logger *slog.Logger) email.Sender {
	if cfg == nil || strings.TrimSpace(cfg.SMTPHost) == "" {
		return nil
	}
	return &email.SMTPSender{
		Host: strings.TrimSpace(cfg.SMTPHost), Port: cfg.SMTPPort,
		Username: cfg.SMTPUsername, Password: cfg.SMTPPassword,
		From: cfg.SMTPFrom, UseTLS: cfg.SMTPTLS,
		Logger: logger,
	}
}

func bootstrapDefaults(ctx context.Context, db *store.DB) error {
	teamSvc := teams.New(db)
	chSvc := channels.New(db)

	team, err := teamSvc.EnsureDefault(ctx)
	if err != nil {
		return err
	}
	ch, err := chSvc.EnsureDefault(ctx, team.ID)
	if err != nil {
		return err
	}

	rows, err := db.Pool.Query(ctx, `
		SELECT id FROM users
		WHERE delete_at = 0
		  AND 'system_user'=ANY(regexp_split_to_array(BTRIM(roles), E'\\s+'))
		  AND NOT ('system_guest'=ANY(regexp_split_to_array(BTRIM(roles), E'\\s+')))
	`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			return err
		}
		if err := teamSvc.Join(ctx, team.ID, uid); err != nil {
			return err
		}
		if err := chSvc.Join(ctx, ch.ID, uid); err != nil {
			return err
		}
	}
	return rows.Err()
}
