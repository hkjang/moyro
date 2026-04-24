package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/moddle/moddle/server/internal/channels"
	"github.com/moddle/moddle/server/internal/config"
	"github.com/moddle/moddle/server/internal/digest"
	"github.com/moddle/moddle/server/internal/email"
	"github.com/moddle/moddle/server/internal/httpapi"
	"github.com/moddle/moddle/server/internal/metrics"
	"github.com/moddle/moddle/server/internal/pluginhost"
	"github.com/moddle/moddle/server/internal/store"
	"github.com/moddle/moddle/server/internal/teams"
	"github.com/moddle/moddle/server/internal/ws"
	"github.com/moddle/moddle/server/internal/ws/redisfanout"
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

	db, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("db open", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := store.Migrate(ctx, db); err != nil {
		logger.Error("migrate", "err", err)
		os.Exit(1)
	}

	// One-shot bootstrap: ensure default team/channel exist and backfill
	// existing users so they have somewhere to chat immediately after login.
	if err := bootstrapDefaults(ctx, db); err != nil {
		logger.Warn("bootstrap defaults", "err", err)
	}

	hub := ws.NewHub()
	go hub.Run(ctx)

	// Feed the moddle_ws_clients prometheus gauge on a slow tick. 5s is
	// fine — the gauge is for trend graphs, not per-request accuracy —
	// and keeps the hub's hot path free of any metrics coupling.
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				metrics.ObserveWSClients(hub.ClientCount())
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

	host := pluginhost.New(cfg.PluginDir, logger)
	if err := host.LoadAll(ctx); err != nil {
		logger.Warn("plugin load", "err", err)
	}

	// Phase 17: email digest. SMTP host empty ⇒ NoopSender so the worker
	// still scans + marks users (keeping last_digest_at fresh) but no
	// mail actually leaves the process. This lets ops toggle SMTP on
	// without a rebuild.
	var mailSender email.Sender
	if cfg.SMTPHost != "" {
		mailSender = &email.SMTPSender{
			Host: cfg.SMTPHost, Port: cfg.SMTPPort,
			Username: cfg.SMTPUsername, Password: cfg.SMTPPassword,
			From: cfg.SMTPFrom, UseTLS: cfg.SMTPTLS,
			Logger: logger,
		}
		logger.Info("smtp sender active", "host", cfg.SMTPHost)
	} else {
		mailSender = &email.NoopSender{Logger: logger}
	}
	digestWorker := digest.NewWorker(db, mailSender, logger, cfg.PublicBaseURL)
	go digestWorker.Run(ctx)

	// Phase 19: build the router plus the scheduled-posts + reminders
	// background workers in one shot. Workers need services the router
	// already owns (posts, files) — keeping construction in one place
	// avoids duplicating the fs-vs-s3 switch.
	backend := httpapi.New(cfg, db, hub, host, logger)
	go backend.Scheduled.Run(ctx)
	go backend.Reminders.Run(ctx)

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           backend.Router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("moddle listening", "addr", cfg.Listen)
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

	rows, err := db.Pool.Query(ctx, `SELECT id FROM users WHERE delete_at = 0`)
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
