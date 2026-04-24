package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/moddle/moddle/server/internal/audit"
	"github.com/moddle/moddle/server/internal/auth"
	"github.com/moddle/moddle/server/internal/bots"
	"github.com/moddle/moddle/server/internal/channels"
	"github.com/moddle/moddle/server/internal/config"
	"github.com/moddle/moddle/server/internal/emojis"
	"github.com/moddle/moddle/server/internal/files"
	"github.com/moddle/moddle/server/internal/invites"
	"github.com/moddle/moddle/server/internal/links"
	"github.com/moddle/moddle/server/internal/metrics"
	"github.com/moddle/moddle/server/internal/oauth"
	"github.com/moddle/moddle/server/internal/pat"
	"github.com/moddle/moddle/server/internal/pluginhost"
	"github.com/moddle/moddle/server/internal/posts"
	"github.com/moddle/moddle/server/internal/ratelimit"
	"github.com/moddle/moddle/server/internal/reactions"
	"github.com/moddle/moddle/server/internal/reminders"
	"github.com/moddle/moddle/server/internal/savedposts"
	"github.com/moddle/moddle/server/internal/scheduled"
	"github.com/moddle/moddle/server/internal/slashcmd"
	"github.com/moddle/moddle/server/internal/store"
	"github.com/moddle/moddle/server/internal/teams"
	"github.com/moddle/moddle/server/internal/userstatus"
	"github.com/moddle/moddle/server/internal/webhooks"
	"github.com/moddle/moddle/server/internal/ws"
)

// Backend bundles the HTTP handler with the background workers the router
// knows how to construct (because it already owns every dependency they
// need). Callers start the workers with `go w.Run(ctx)` alongside the HTTP
// server. Keeping the construction here means `main.go` doesn't have to
// duplicate the fs-vs-s3 storage switch or re-instantiate shared services.
type Backend struct {
	Router    http.Handler
	Scheduled *scheduled.Worker
	Reminders *reminders.Worker
}

// NewRouter is the legacy entry point — returns just the http.Handler.
// Kept for test helpers that don't care about the workers.
func NewRouter(cfg *config.Config, db *store.DB, hub *ws.Hub, host *pluginhost.Host, logger *slog.Logger) http.Handler {
	return New(cfg, db, hub, host, logger).Router
}

// New wires everything: HTTP routes plus the scheduled-posts and reminders
// workers. Workers are wired but NOT started — the caller decides when to
// start them (main.go attaches them to its shutdown context).
func New(cfg *config.Config, db *store.DB, hub *ws.Hub, host *pluginhost.Host, logger *slog.Logger) *Backend {
	authSvc := auth.New(db, cfg.JWTSecret, cfg.TokenTTL)
	teamSvc := teams.New(db)
	channelSvc := channels.New(db)
	postSvc := posts.New(db)
	reactionSvc := reactions.New(db)
	// Pick file backend based on MODDLE_FILE_BACKEND. Anything other than
	// "s3" (incl. empty / "fs") keeps the local filesystem impl. An S3
	// dial failure falls back to FS with a warning — the plan explicitly
	// calls fail-open for infra hiccups so the server still boots.
	var fileStorage files.Storage = files.NewFSStorage(cfg.FileStorageRoot)
	if cfg.FileBackend == "s3" {
		if s3st, err := files.NewS3Storage(context.Background(), cfg.S3Bucket, cfg.S3Region, cfg.S3Endpoint); err != nil {
			logger.Warn("s3 storage disabled; falling back to fs", "err", err)
		} else {
			fileStorage = s3st
			logger.Info("s3 storage active", "bucket", cfg.S3Bucket, "endpoint", cfg.S3Endpoint)
		}
	}
	fileSvc := files.New(db, fileStorage)
	statusSvc := userstatus.New(db)
	auditSvc := audit.New(db, logger)
	slashSvc := slashcmd.New(postSvc, channelSvc, statusSvc, &pluginCommandAdapter{host: host})
	botSvc := bots.New(db)
	incomingSvc := webhooks.NewIncoming(db, postSvc)
	outgoingSvc := webhooks.NewOutgoing(db)
	emojiSvc := emojis.New(db, fileSvc)
	oauthReg := oauth.NewRegistry(cfg)
	oauthIdent := oauth.NewIdentityStore(db, authSvc)
	inviteSvc := invites.New(db)
	savedSvc := savedposts.New(db)
	// Phase 19: scheduled messages + post reminders. Services are cheap
	// DB wrappers; their Workers are started from main so they live with
	// the process, not with the HTTP router.
	scheduledSvc := scheduled.New(db)
	reminderSvc := reminders.New(db)
	// Phase 18: link preview fetcher. Nil when disabled; handlers check
	// for nil before kicking off the async fetch so a feature-flagged-off
	// deploy skips the goroutine entirely.
	var linkSvc *links.Service
	if cfg.LinkPreviewsEnabled {
		linkSvc = links.New()
	}

	// Resolve a channel id → team id through the channels.Service without
	// making webhooks depend on it at compile time.
	teamOf := func(ctx context.Context, channelID string) (string, error) {
		ch, err := channelSvc.Get(ctx, channelID)
		if err != nil {
			return "", err
		}
		return ch.TeamID, nil
	}
	outDispatcher := webhooks.NewDispatcher(outgoingSvc, postSvc, teamOf, logger, 16, cfg.AllowedOutgoingHosts)

	// Feed the moddle_webhook_queue_depth prometheus gauge on a slow tick.
	// Running inside NewRouter keeps the dispatcher from knowing about
	// metrics at the type level; the goroutine lives for the process.
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for range t.C {
			metrics.ObserveWebhookQueue(outDispatcher.QueueDepth())
		}
	}()

	h := &handlers{
		cfg:       cfg,
		auth:      authSvc,
		teams:     teamSvc,
		channels:  channelSvc,
		posts:     postSvc,
		reactions: reactionSvc,
		files:     fileSvc,
		status:    statusSvc,
		audit:     auditSvc,
		slash:     slashSvc,
		bots:      botSvc,
		incoming:  incomingSvc,
		outgoing:  outgoingSvc,
		outDisp:   outDispatcher,
		emojis:    emojiSvc,
		oauthReg:  oauthReg,
		oauthIdent: oauthIdent,
		invites:   inviteSvc,
		saved:     savedSvc,
		links:     linkSvc,
		scheduled: scheduledSvc,
		reminders: reminderSvc,
		hub:       hub,
		host:      host,
		logger:    logger,
	}

	// Auto-presence: drive the user's status from socket lifecycle, but
	// never overwrite an explicitly chosen state (DND/away). SetAuto's
	// guard handles that in SQL so we don't need to re-read first.
	hub.OnConnect = func(userID string) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		st, err := statusSvc.SetAuto(ctx, userID, userstatus.Online)
		if err != nil || st == nil {
			return
		}
		raw, _ := json.Marshal(st)
		hub.Broadcast(ws.Event{
			Event: "status_change",
			Data: map[string]any{
				"user_id": st.UserID,
				"status":  st.Status,
				"payload": string(raw),
			},
		})
	}
	hub.OnDisconnect = func(userID string) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		st, err := statusSvc.SetAuto(ctx, userID, userstatus.Offline)
		if err != nil || st == nil {
			return
		}
		raw, _ := json.Marshal(st)
		hub.Broadcast(ws.Event{
			Event: "status_change",
			Data: map[string]any{
				"user_id": st.UserID,
				"status":  st.Status,
				"payload": string(raw),
			},
		})
	}

	// Per-user write limiter: 30 req/s sustained, burst 60. Generous for
	// real usage; harsh enough to choke a runaway script without tuning.
	userLimiter := ratelimit.New(30, 60)
	// Per-IP login limiter: 1 req/s sustained, burst 5. Slows brute-force
	// password guessing without disturbing an honest user who mistypes.
	loginLimiter := ratelimit.New(1, 5)
	// Per-IP incoming-webhook limiter: public surface, must be tight.
	hookIPLimiter := ratelimit.New(5, 10)
	// Per-IP OAuth limiter: the login kickoff is cheap but the callback
	// does provider HTTP calls + DB writes, so we cap both at the same
	// rate an honest user could never hit.
	oauthIPLimiter := ratelimit.New(5, 10)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))
	r.Use(requestLog(logger))
	// Prometheus HTTP duration histogram. Registered after chi's route
	// resolver so RoutePattern() is available for the path label.
	r.Use(metrics.HTTPMiddleware())

	// Operator endpoints — intentionally outside /api/v4 and the auth
	// chain so probes + scrapers don't need credentials.
	r.Get("/healthz", h.healthz)
	r.Method(http.MethodGet, "/metrics", metrics.Handler())

	// Public incoming webhook endpoint. Kept OUTSIDE /api/v4 and outside
	// the auth chain so clients can POST with a bare URL. A per-IP
	// limiter handles abuse; body size is capped in the handler.
	r.With(hookIPLimiter.Middleware(ratelimit.ClientIP)).
		Post("/hooks/{hookID}", h.fireIncomingWebhook)

	// PAT pre-middleware: if the bearer token begins with "mdp_", resolve
	// it to a user id and inject into context. Downstream requireAuth
	// detects the pre-set id and skips JWT parsing.
	patMW := pat.With(botSvc, SetUserIDOnContext)

	r.Route("/api/v4", func(r chi.Router) {
		r.Get("/system/ping", h.ping)

		// OAuth endpoints are public by design: /login kicks off the flow
		// before we know who the user is, and /callback is reached via
		// provider redirect (no JWT yet). Per-IP rate-limited so a scripted
		// state-guessing attack can't exhaust DB connections.
		r.With(oauthIPLimiter.Middleware(ratelimit.ClientIP)).
			Get("/oauth/{provider}/login", h.oauthLogin)
		r.With(oauthIPLimiter.Middleware(ratelimit.ClientIP)).
			Get("/oauth/{provider}/callback", h.oauthCallback)

		r.Post("/users", h.register)
		r.With(loginLimiter.Middleware(ratelimit.ClientIP)).Post("/users/login", h.login)

		// Public invite preview. Reveals only the team display name + basic
		// invite metadata so the signup form can say "You're joining X".
		// Rate-limited per-IP because this is unauthenticated surface.
		r.With(loginLimiter.Middleware(ratelimit.ClientIP)).
			Get("/invites/{inviteID}", h.getInvite)

		r.Group(func(r chi.Router) {
			r.Use(patMW)
			r.Use(h.requireAuth)
			r.Use(userLimiter.Middleware(func(req *http.Request) string {
				return userID(req)
			}))

			r.Get("/users/me", h.me)
			r.Put("/users/me", h.updateProfile)
			r.Put("/users/me/password", h.updatePassword)
			// Profile picture: POST /users/me/image (self-upload only).
			// GET /users/{userID}/image is readable by any authenticated
			// user so every peer's avatar renders in message rows.
			r.Post("/users/me/image", h.uploadProfileImage)
			r.Post("/users/logout", h.logout)
			// Phase 17: email digest preferences (opt-out toggle).
			r.Get("/users/me/email_prefs", h.getMyEmailPrefs)
			r.Put("/users/me/email_prefs", h.updateMyEmailPrefs)
			// Phase 16: self-serve session management.
			r.Get("/users/me/sessions", h.listMySessions)
			r.Delete("/users/me/sessions/{sessionID}", h.revokeMySession)
			r.Delete("/users/me/sessions", h.revokeMyOtherSessions)
			r.Get("/users", h.listUsers)
			r.Post("/users/search", h.searchUsers)
			r.Get("/users/{userID}", h.getUser)
			r.Get("/users/username/{username}", h.getUserByUsername)
			r.Get("/users/{userID}/image", h.getUserImage)
			// Phase 16: soft-delete / restore. DELETE accepts self-for-self
			// so a regular user can close their own account; handler
			// enforces the admin-or-self check. Reactivate is admin-only.
			r.Delete("/users/{userID}", h.deactivateUser)
			r.Get("/users/{userID}/status", h.getUserStatus)
			r.Post("/users/statuses/ids", h.getUserStatusesByIDs)
			r.Put("/users/me/status", h.updateMyStatus)
			r.Post("/teams", h.createTeam)
			r.Get("/users/me/teams", h.listTeams)
			r.Post("/teams/{teamID}/posts/search", h.searchPosts)

			// Team invite CRUD. Handler performs the "caller must be
			// team_admin or system_admin" check itself because chi can't
			// express param-dependent authorization declaratively.
			r.Post("/teams/{teamID}/invites", h.createInvite)
			r.Get("/teams/{teamID}/invites", h.listInvites)
			r.Delete("/teams/{teamID}/invites/{inviteID}", h.revokeInvite)

			r.Post("/channels", h.createChannel)
			r.Get("/users/me/teams/{teamID}/channels", h.listChannels)
			r.Post("/channels/direct", h.createDirectChannel)
			r.Get("/channels/{channelID}", h.getChannel)
			r.Put("/channels/{channelID}", h.patchChannel)
			r.Post("/channels/{channelID}/view", h.viewChannel)

			r.Post("/posts", h.createPost)
			r.Get("/channels/{channelID}/posts", h.listPosts)
			r.Put("/posts/{postID}", h.updatePost)
			r.Delete("/posts/{postID}", h.deletePost)
			r.Post("/posts/{postID}/pin", h.pinPost)
			r.Post("/posts/{postID}/unpin", h.unpinPost)
			r.Get("/channels/{channelID}/pinned", h.listPinned)
			r.Get("/posts/{postID}/thread", h.listThread)

			// Phase 18 — saved posts (personal bookmarks).
			r.Get("/users/me/saved_posts", h.listSavedPosts)
			r.Post("/users/me/saved_posts/ids", h.savedPostsBulkCheck)
			r.Post("/users/me/saved_posts/{postID}", h.savePost)
			r.Delete("/users/me/saved_posts/{postID}", h.unsavePost)

			// Phase 18 — public channel discovery.
			r.Get("/teams/{teamID}/channels/discover", h.discoverChannels)

			// Phase 18 — link preview image proxy. Keeps third-party pixel
			// trackers off the client; re-applies the RFC1918 guard so a
			// maliciously-crafted image URL can't SSRF from the server.
			r.Get("/link_preview_image", h.linkPreviewImage)

			// Phase 19 — scheduled messages.
			r.Post("/scheduled_posts", h.createScheduledPost)
			r.Get("/users/me/scheduled_posts", h.listMyScheduledPosts)
			r.Delete("/scheduled_posts/{scheduledID}", h.deleteScheduledPost)

			// Phase 19 — post reminders.
			r.Post("/posts/{postID}/remind_me", h.createPostReminder)
			r.Get("/users/me/reminders", h.listMyReminders)
			r.Delete("/users/me/reminders/{reminderID}", h.deleteReminder)

			r.Post("/reactions", h.addReaction)
			r.Get("/posts/{postID}/reactions", h.listReactions)
			r.Delete("/users/{userID}/posts/{postID}/reactions/{emoji}", h.removeReaction)

			r.Post("/files", h.uploadFiles)
			r.Get("/files/{fileID}", h.downloadFile)
			r.Get("/files/{fileID}/info", h.fileInfo)
			r.Get("/files/{fileID}/thumbnail", h.fileThumbnail)

			// Custom emoji (Phase 13). Any logged-in user can create; only
			// the creator (or admin) can delete. Images are globally visible
			// since they're identifiers, not private content.
			r.Post("/emoji", h.createEmoji)
			r.Get("/emoji", h.listEmojis)
			r.Get("/emoji/{emojiID}", func(w http.ResponseWriter, r *http.Request) {
				// Inline wrapper so both id→record and name→record share one URL style.
				id := chi.URLParam(r, "emojiID")
				e, err := h.emojis.Get(r.Context(), id)
				if err != nil {
					writeError(w, 404, "api.emoji.get.not_found", err.Error())
					return
				}
				writeJSON(w, 200, e)
			})
			r.Get("/emoji/name/{name}", h.getEmojiByName)
			r.Get("/emoji/{emojiID}/image", h.getEmojiImage)
			r.Delete("/emoji/{emojiID}", h.deleteEmoji)

			r.Get("/channels/{channelID}/members", h.listChannelMembers)
			// /members/autocomplete must be registered before the bare
			// /members catch-all; chi matches in registration order but
			// specific paths before parameterized ones is safest.
			r.Get("/channels/{channelID}/members/autocomplete", h.channelMembersAutocomplete)
			r.Post("/channels/{channelID}/members", h.addChannelMember)
			// Phase 18 — self-join for public channel discovery. Distinct
			// from addChannelMember (which requires already being a member
			// to add others) so outsiders can join public channels.
			r.Post("/channels/{channelID}/join", h.selfJoinChannel)
			r.Delete("/channels/{channelID}/members/{userID}", h.removeChannelMember)
			r.Get("/channels/{channelID}/members/me/notify_props", h.getMyNotifyProps)
			r.Put("/channels/{channelID}/members/me/notify_props", h.putMyNotifyProps)
			r.Get("/users/me/teams/{teamID}/channels/members", h.listMyChannelMembers)

			r.Post("/commands/execute", h.executeCommand)

			// Personal access tokens — self-issue allowed. The handler
			// performs the admin/self check itself since chi can't express
			// "caller must equal :userID parameter" declaratively.
			r.Post("/users/{userID}/tokens", h.createToken)
			r.Get("/users/{userID}/tokens", h.listTokens)

			// Operator surfaces — restricted to system_admin so a
			// regular user can't browse audit trails or the plugin
			// inventory.
			r.Group(func(r chi.Router) {
				r.Use(h.requireRole("system_admin"))
				r.Get("/plugins", h.listPlugins)
				r.Get("/audit/logs", h.listAudit)

				// Phase 16 admin actions
				r.Post("/users/{userID}/reactivate", h.reactivateUser)
				r.Post("/channels/{channelID}/archive", h.archiveChannel)
				r.Post("/channels/{channelID}/restore", h.restoreChannel)

				// Bot CRUD
				r.Post("/bots", h.createBot)
				r.Get("/bots", h.listBots)
				r.Delete("/bots/{botID}", h.disableBot)
				r.Post("/tokens/{tokenID}/revoke", h.revokeToken)

				// Incoming webhook CRUD (the fire endpoint is public,
				// mounted above).
				r.Post("/hooks/incoming", h.createIncomingWebhook)
				r.Get("/hooks/incoming", h.listIncomingWebhooks)
				r.Delete("/hooks/incoming/{hookID}", h.deleteIncomingWebhook)

				// Outgoing webhook CRUD
				r.Post("/hooks/outgoing", h.createOutgoingWebhook)
				r.Get("/hooks/outgoing", h.listOutgoingWebhooks)
				r.Delete("/hooks/outgoing/{hookID}", h.deleteOutgoingWebhook)
			})
		})
	})

	r.HandleFunc("/api/v4/websocket", h.websocket)

	scheduledWorker := scheduled.NewWorker(scheduledSvc, postSvc, fileSvc, hub, logger)
	remindersWorker := reminders.NewWorker(reminderSvc, postSvc, hub, logger)

	return &Backend{Router: r, Scheduled: scheduledWorker, Reminders: remindersWorker}
}

func requestLog(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			logger.Info("http",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"dur_ms", time.Since(start).Milliseconds(),
			)
		})
	}
}
