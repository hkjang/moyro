package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/hkjang/moyro/server/internal/audit"
	"github.com/hkjang/moyro/server/internal/auth"
	"github.com/hkjang/moyro/server/internal/bookmarks"
	"github.com/hkjang/moyro/server/internal/bots"
	"github.com/hkjang/moyro/server/internal/channels"
	"github.com/hkjang/moyro/server/internal/commands"
	"github.com/hkjang/moyro/server/internal/config"
	"github.com/hkjang/moyro/server/internal/customprofile"
	"github.com/hkjang/moyro/server/internal/emojis"
	"github.com/hkjang/moyro/server/internal/files"
	"github.com/hkjang/moyro/server/internal/invites"
	"github.com/hkjang/moyro/server/internal/links"
	"github.com/hkjang/moyro/server/internal/metrics"
	"github.com/hkjang/moyro/server/internal/oauth"
	"github.com/hkjang/moyro/server/internal/pat"
	"github.com/hkjang/moyro/server/internal/pluginhost"
	"github.com/hkjang/moyro/server/internal/postacks"
	"github.com/hkjang/moyro/server/internal/posts"
	"github.com/hkjang/moyro/server/internal/preferences"
	"github.com/hkjang/moyro/server/internal/ratelimit"
	"github.com/hkjang/moyro/server/internal/reactions"
	"github.com/hkjang/moyro/server/internal/registration"
	"github.com/hkjang/moyro/server/internal/reminders"
	"github.com/hkjang/moyro/server/internal/savedposts"
	"github.com/hkjang/moyro/server/internal/scheduled"
	"github.com/hkjang/moyro/server/internal/sidebar"
	"github.com/hkjang/moyro/server/internal/slashcmd"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/hkjang/moyro/server/internal/teams"
	"github.com/hkjang/moyro/server/internal/threads"
	"github.com/hkjang/moyro/server/internal/tos"
	"github.com/hkjang/moyro/server/internal/userstatus"
	"github.com/hkjang/moyro/server/internal/webhooks"
	"github.com/hkjang/moyro/server/internal/webui"
	"github.com/hkjang/moyro/server/internal/ws"
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
	Approvals *ApprovalExecutor
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
	hub.SetAudienceResolver(ws.DatabaseAudienceResolver(db))
	authSvc := auth.New(db, cfg.JWTSecret, cfg.TokenTTL)
	teamSvc := teams.New(db)
	channelSvc := channels.New(db)
	postSvc := posts.New(db)
	reactionSvc := reactions.New(db)
	// Pick the fixed offline-safe filesystem backend by default. A future
	// database setting may select an administrator-configured S3 endpoint.
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
	registrationSvc := registration.New(db)
	savedSvc := savedposts.New(db)
	// Phase 19: scheduled messages + post reminders. Services are cheap
	// DB wrappers; their Workers are started from main so they live with
	// the process, not with the HTTP router.
	scheduledSvc := scheduled.New(db)
	reminderSvc := reminders.New(db)
	// Phase 21: Mattermost-shaped preferences. Pure DB CRUD; no worker.
	prefsSvc := preferences.New(db)
	// Phase 22: channel sidebar categories. Auto-bootstraps three defaults
	// (favorites/channels/direct_messages) per (user, team) on first list.
	sidebarSvc := sidebar.New(db)
	// Phase 23: custom slash-command CRUD and autocomplete.
	commandSvc := commands.New(db)
	// Phase 24: thread membership store. One row per (user, root); team_id
	// denormalised so "mark all in team read" doesn't have to walk posts.
	threadSvc := threads.New(db)
	// Phase 29: channel bookmarks (pinned URL + file links above the
	// message stream). Channel-scoped, soft-deleted, sort-orderable.
	bookmarkSvc := bookmarks.New(db)
	// Phase 32: custom profile attributes (admin-defined extra fields on
	// every user's profile). Two tables — fields + values; values are
	// raw JSONB so future field types round-trip without a migration.
	customProfileSvc := customprofile.New(db)
	// Phase 33: post acknowledgements + terms-of-service durability. Both
	// were Phase 28 in-memory stubs; now real schema-backed services so a
	// server restart no longer wipes ack state or the active TOS body.
	postacksSvc := postacks.New(db)
	tosSvc := tos.New(db)
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

	// Feed the moyro_webhook_queue_depth prometheus gauge on a slow tick.
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
		cfg:          cfg,
		auth:         authSvc,
		teams:        teamSvc,
		channels:     channelSvc,
		posts:        postSvc,
		reactions:    reactionSvc,
		files:        fileSvc,
		status:       statusSvc,
		audit:        auditSvc,
		slash:        slashSvc,
		bots:         botSvc,
		incoming:     incomingSvc,
		outgoing:     outgoingSvc,
		outDisp:      outDispatcher,
		emojis:       emojiSvc,
		oauthReg:     oauthReg,
		oauthIdent:   oauthIdent,
		invites:      inviteSvc,
		registration: registrationSvc,
		saved:        savedSvc,
		links:        linkSvc,
		scheduled:    scheduledSvc,
		reminders:    reminderSvc,
		prefs:        prefsSvc,
		sidebar:      sidebarSvc,
		commands:     commandSvc,
		threads:      threadSvc,
		bookmarks:    bookmarkSvc,
		customProf:   customProfileSvc,
		postacks:     postacksSvc,
		tos:          tosSvc,
		hub:          hub,
		host:         host,
		logger:       logger,
	}
	nativeCtx, nativeCancel := context.WithTimeout(context.Background(), 15*time.Second)
	if native, err := newNativeServices(nativeCtx, cfg, db, h, logger); err != nil {
		logger.Warn("moyro management services unavailable", "err", err)
	} else {
		h.native = native
	}
	nativeCancel()

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
	// Account creation performs bcrypt work and writes several rows. Keep a
	// separate, tighter bucket so opening local signup cannot be used as a
	// cheap CPU or database exhaustion primitive.
	signupLimiter := ratelimit.New(0.2, 3)
	// Per-IP incoming-webhook limiter: public surface, must be tight.
	hookIPLimiter := ratelimit.New(5, 10)
	// Per-IP OAuth limiter: the login kickoff is cheap but the callback
	// does provider HTTP calls + DB writes, so we cap both at the same
	// rate an honest user could never hit.
	oauthIPLimiter := ratelimit.New(5, 10)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	// Do not trust X-Forwarded-For, X-Real-IP, or True-Client-IP by default.
	// The four-variable runtime contract has no trusted-proxy allow-list, so
	// accepting those headers from a directly exposed container would let an
	// attacker rotate the rate-limit and audit address at will. Peer
	// RemoteAddr remains the authoritative address in v0.1.0.
	r.Use(middleware.Recoverer)
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
		r.Use(middleware.Timeout(30 * time.Second))
		r.Get("/system/ping", h.ping)
		r.Get("/config/client", h.getClientConfig)
		r.Get("/license/client", h.getClientLicense)

		// OAuth endpoints are public by design: /login kicks off the flow
		// before we know who the user is, and /callback is reached via
		// provider redirect (no JWT yet). Per-IP rate-limited so a scripted
		// state-guessing attack can't exhaust DB connections.
		r.With(oauthIPLimiter.Middleware(ratelimit.ClientIP)).
			Get("/oauth/{provider}/login", h.oauthLogin)
		r.With(oauthIPLimiter.Middleware(ratelimit.ClientIP)).
			Get("/oauth/{provider}/callback", h.oauthCallback)

		r.With(signupLimiter.Middleware(ratelimit.ClientIP)).Post("/users", h.register)
		r.With(loginLimiter.Middleware(ratelimit.ClientIP)).Post("/users/login", h.login)

		// Public invite preview. Reveals only the team display name + basic
		// invite metadata so the signup form can say "You're joining X".
		// Rate-limited per-IP because this is unauthenticated surface.
		r.With(loginLimiter.Middleware(ratelimit.ClientIP)).
			Get("/invites/{inviteID}", h.getInvite)

		// Phase 27 — Mattermost API v4 compatibility wave 7.
		//
		// Pillar C — Email verify + password reset stubs. Public surface
		// (no auth) since Mattermost's spec puts these on the unauthenticated
		// path — that's the "I forgot my password" flow. Rate-limited per-IP
		// to prevent enumeration. Currently 200 OK stubs because we don't
		// run an SMTP-driven verify/reset path yet. Single-line registration
		// form so the audit regex picks them up cleanly.
		r.Group(func(r chi.Router) {
			r.Use(loginLimiter.Middleware(ratelimit.ClientIP))
			r.Post("/users/email/verify", h.verifyUserEmail)
			r.Post("/users/email/verify/send", h.sendUserEmailVerification)
			r.Post("/users/password/reset", h.consumePasswordReset)
			r.Post("/users/password/reset/send", h.sendPasswordResetEmail)
			// Public invite preview alias. Mattermost ships both
			// `/invites/{id}` and `/teams/invite/{id}` shapes; aliasing
			// to the same handler avoids handler drift.
			r.Get("/teams/invite/{inviteID}", h.getInvite)
		})

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
			r.Get("/users/stats", h.getUserStats)
			r.Get("/users/{userID}", h.getUser)
			r.Put("/users/{userID}", h.updateUserFull)
			r.Put("/users/{userID}/patch", h.patchUser)
			r.Put("/users/{userID}/active", h.setUserActive)
			r.Delete("/users/{userID}/image", h.deleteUserImage)
			r.Get("/users/username/{username}", h.getUserByUsername)
			r.Get("/users/{userID}/image/default", h.getDefaultProfileImage)
			r.Get("/users/{userID}/image", h.getUserImage)
			r.Get("/users/{userID}/sessions", h.listUserSessions)
			// Phase 16: soft-delete / restore. DELETE accepts self-for-self
			// so a regular user can close their own account; handler
			// enforces the admin-or-self check. Reactivate is admin-only.
			r.Delete("/users/{userID}", h.deactivateUser)
			r.Get("/users/{userID}/status", h.getUserStatus)
			r.Put("/users/{userID}/status", h.updateUserStatus)
			r.Post("/users/status/ids", h.getUserStatusesByIDs)
			r.Post("/users/statuses/ids", h.getUserStatusesByIDs)
			r.Put("/users/me/status", h.updateMyStatus)
			r.Get("/system/timezones", h.getSupportedTimezones)
			r.Post("/teams", h.createTeam)
			r.Get("/teams", h.listTeams)
			r.Get("/teams/{teamID}", h.getTeam)
			r.Get("/users/me/teams", h.listTeams)
			r.Get("/users/{userID}/teams", h.listTeamsForUserParam)
			r.Get("/users/{userID}/teams/members", h.listUserTeamMembers)
			r.Get("/users/{userID}/teams/unread", h.listUserTeamsUnread)
			r.Get("/users/{userID}/teams/{teamID}/unread", h.getUserTeamUnread)
			r.Post("/teams/{teamID}/posts/search", h.searchPosts)

			// Team invite CRUD. Handler performs the "caller must be
			// team_admin or system_admin" check itself because chi can't
			// express param-dependent authorization declaratively.
			r.Post("/teams/{teamID}/invites", h.createInvite)
			r.Get("/teams/{teamID}/invites", h.listInvites)
			r.Delete("/teams/{teamID}/invites/{inviteID}", h.revokeInvite)

			r.Post("/channels", h.createChannel)
			r.Get("/teams/{teamID}/channels", h.listChannels)
			r.Get("/users/me/teams/{teamID}/channels", h.listChannels)
			r.Get("/users/{userID}/teams/{teamID}/channels", h.listChannelsForUserParam)
			r.Get("/users/{userID}/channels", h.listChannelsForUserParam)
			r.Get("/users/{userID}/channels/{channelID}/unread", h.getChannelUnread)
			r.Post("/channels/direct", h.createDirectChannel)
			r.Get("/channels/{channelID}", h.getChannel)
			r.Put("/channels/{channelID}", h.patchChannel)
			r.Put("/channels/{channelID}/patch", h.patchChannelExtended)
			r.Put("/channels/{channelID}/privacy", h.updateChannelPrivacy)
			r.Post("/channels/{channelID}/view", h.viewChannel)

			r.Post("/posts", h.createPost)
			r.Get("/posts/{postID}", h.getPost)
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
			r.Post("/posts/schedule", h.createSchedulePostAlias)
			r.Get("/posts/scheduled/team/{teamID}", h.listScheduledPostsForTeam)
			r.Put("/posts/schedule/{scheduledID}", h.updateSchedulePost)
			r.Delete("/posts/schedule/{scheduledID}", h.deleteSchedulePostAlias)

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
			r.Get("/files/{fileID}/link", h.fileLink)
			r.Get("/files/{fileID}/preview", h.filePreview)
			r.Get("/files/{fileID}/thumbnail", h.fileThumbnail)

			// Custom emoji (Phase 13). Any logged-in user can create; only
			// the creator (or admin) can delete. Images are globally visible
			// since they're identifiers, not private content.
			r.Post("/emoji", h.createEmoji)
			r.Get("/emoji", h.listEmojis)
			r.Get("/emoji/autocomplete", h.autocompleteEmojis)
			r.Post("/emoji/search", h.searchEmojis)
			r.Post("/emoji/names", h.emojisByNames)
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
			r.Get("/channels/{channelID}/members/{targetUserID}", h.getChannelMember)
			r.Post("/channels/{channelID}/members", h.addChannelMember)
			r.Post("/channels/{channelID}/members/ids", h.channelMembersByUserIDs)
			r.Post("/channels/members/{userID}/view", h.viewChannelForUser)
			// Phase 18 — self-join for public channel discovery. Distinct
			// from addChannelMember (which requires already being a member
			// to add others) so outsiders can join public channels.
			r.Post("/channels/{channelID}/join", h.selfJoinChannel)
			r.Delete("/channels/{channelID}/members/{userID}", h.removeChannelMember)
			r.Get("/channels/{channelID}/members/me/notify_props", h.getMyNotifyProps)
			r.Put("/channels/{channelID}/members/me/notify_props", h.putMyNotifyProps)
			r.Get("/users/me/teams/{teamID}/channels/members", h.listMyChannelMembers)
			r.Get("/users/{userID}/teams/{teamID}/channels/members", h.listChannelMembersForUserParam)

			// Phase 21 — Mattermost API v4 compatibility wave 1.
			//
			// Preferences (5 endpoints): the canonical contract official
			// clients use to sync theme, sidebar, favorites, tutorial steps.
			r.Get("/users/{userID}/preferences", h.listAllPreferences)
			r.Put("/users/{userID}/preferences", h.upsertPreferences)
			r.Post("/users/{userID}/preferences/delete", h.deletePreferences)
			r.Get("/users/{userID}/preferences/{category}", h.listPreferencesInCategory)
			r.Get("/users/{userID}/preferences/{category}/name/{name}", h.getPreferenceByName)

			// Users compat: autocomplete + bulk hydrate + email lookup.
			// `/users/autocomplete` MUST be registered BEFORE `/users/{userID}`
			// in chi's tree to win the match — chi orders specific paths
			// before parameterized ones automatically, but we keep this
			// block adjacent for clarity.
			r.Get("/users/autocomplete", h.autocompleteUsers)
			r.Post("/users/ids", h.usersByIDs)
			r.Post("/users/usernames", h.usersByUsernames)
			r.Get("/users/email/{email}", h.getUserByEmail)

			// Teams compat: stats + name lookup + members.
			r.Get("/teams/name/{name}", h.getTeamByName)
			r.Get("/teams/{teamID}/stats", h.getTeamStats)
			r.Get("/teams/{teamID}/members", h.listTeamMembers)
			r.Post("/teams/{teamID}/members", h.addTeamMember)
			r.Post("/teams/{teamID}/members/batch", h.addTeamMembersBatch)
			r.Post("/teams/{teamID}/members/ids", h.teamMembersByIDs)
			r.Get("/teams/{teamID}/members/{userID}", h.getTeamMember)

			// Channels compat: stats + name lookup + search + autocomplete.
			r.Get("/channels/{channelID}/stats", h.getChannelStats)
			r.Get("/teams/{teamID}/channels/name/{channelName}", h.getChannelByName)
			r.Post("/teams/{teamID}/channels/search", h.searchChannelsInTeam)
			r.Get("/teams/{teamID}/channels/autocomplete", h.autocompleteChannelsInTeam)
			r.Get("/teams/{teamID}/commands/autocomplete", h.autocompleteCommandsForTeam)

			// Posts compat: bulk-by-ids + patch alias.
			r.Post("/posts/ids", h.postsByIDs)
			r.Put("/posts/{postID}/patch", h.patchPost)

			// Phase 22 — Mattermost API v4 compatibility wave 2.
			//
			// Channel sidebar categories (8 endpoints). The official desktop
			// and webapp clients drive the sidebar through these — without
			// them the channel list defaults to a single ungrouped column
			// and starring/dragging silently fails.
			r.Get("/users/{userID}/teams/{teamID}/channels/categories", h.listSidebarCategories)
			r.Post("/users/{userID}/teams/{teamID}/channels/categories", h.createSidebarCategory)
			r.Put("/users/{userID}/teams/{teamID}/channels/categories", h.updateSidebarCategoriesBulk)
			r.Get("/users/{userID}/teams/{teamID}/channels/categories/order", h.listSidebarCategoryOrder)
			r.Put("/users/{userID}/teams/{teamID}/channels/categories/order", h.updateSidebarCategoryOrder)
			r.Get("/users/{userID}/teams/{teamID}/channels/categories/{categoryID}", h.getSidebarCategory)
			r.Put("/users/{userID}/teams/{teamID}/channels/categories/{categoryID}", h.updateSidebarCategory)
			r.Delete("/users/{userID}/teams/{teamID}/channels/categories/{categoryID}", h.deleteSidebarCategory)

			// User notify_props (top-level, distinct from per-channel
			// notify_props). Mattermost stores email/desktop/push/first_name
			// flags here; the webapp's notification panel writes through
			// these endpoints.
			r.Get("/users/{userID}/notify_props", h.getUserNotifyProps)
			r.Put("/users/{userID}/notify_props", h.putUserNotifyProps)

			// Team search + name-exists probe. `exists` is used by signup
			// forms to validate the slug client-side before submitting.
			r.Post("/teams/search", h.searchTeams)
			r.Get("/teams/name/{name}/exists", h.teamNameExists)

			// User channel_members hydration — bulk-read every channel
			// membership for one user in a single round-trip.
			r.Get("/users/{userID}/channel_members", h.listUserChannelMembers)
			r.Post("/users/{userID}/channels/members", h.channelMembersByIDs)

			// Phase 24 — Mattermost API v4 compatibility wave 4 (auth chain).
			//
			// Pillar A — Threads compat. The official desktop reads/writes
			// thread membership state through these three; without them the
			// "Threads" view degrades to client-only state that doesn't
			// survive a relogin.
			r.Put("/users/{userID}/teams/{teamID}/threads/{rootID}/following", h.putThreadFollowing)
			r.Put("/users/{userID}/teams/{teamID}/threads/{rootID}/read/{timestamp}", h.putThreadRead)
			r.Put("/users/{userID}/teams/{teamID}/threads/read", h.putAllThreadsRead)

			// Pillar B — Teams CRUD. Caller must be team_admin (or
			// system_admin); the handler enforces it directly.
			r.Put("/teams/{teamID}", h.updateTeamFull)
			r.Put("/teams/{teamID}/patch", h.patchTeam)
			r.Put("/teams/{teamID}/privacy", h.updateTeamPrivacy)

			// Pillar D — User admin (roles + password + device). The
			// password handler dispatches admin-vs-self internally; roles
			// and device are gated within the handler.
			r.Put("/users/{userID}/roles", h.setUserRoles)
			r.Put("/users/{userID}/password", h.adminSetPassword)
			r.Put("/users/sessions/device", h.setDeviceID)
			r.Put("/users/{userID}/sessions/device", h.setDeviceID)

			// Pillar E — Custom status. Self-only (the param gate restricts
			// non-admin to their own row).
			r.Put("/users/{userID}/status/custom", h.setCustomStatus)
			r.Delete("/users/{userID}/status/custom", h.clearCustomStatus)

			// Pillar F — Set unread. Member-only (handler verifies before
			// rewinding the read marker).
			r.Post("/users/{userID}/posts/{postID}/set_unread", h.setPostUnread)

			// Phase 25 — Mattermost API v4 compatibility wave 5.
			//
			// Pillar A — Channel + team member roles + per-member
			// notify_props peer-write. Handlers do their own admin gate
			// (channel_admin / team_admin / system_admin); the chi route
			// itself sits inside the auth chain so JWT presence is
			// enforced.
			r.Put("/channels/{channelID}/members/{userID}/roles", h.setChannelMemberRoles)
			r.Put("/channels/{channelID}/members/{userID}/schemeRoles", h.setChannelMemberRoles)
			r.Put("/channels/{channelID}/members/{userID}/notify_props", h.setChannelMemberNotifyProps)
			r.Put("/teams/{teamID}/members/{userID}/roles", h.setTeamMemberRoles)
			r.Put("/teams/{teamID}/members/{userID}/schemeRoles", h.setTeamMemberRoles)

			// Pillar B — Session revocation HTTP fallbacks. The /me/
			// variants in /users/me/sessions cover the bearer-token-owner
			// case; these accept an explicit user param so admins can
			// peer-revoke without spoofing the target's bearer.
			r.Post("/users/{userID}/sessions/revoke", h.revokeUserSession)
			r.Post("/users/{userID}/sessions/revoke/all", h.revokeAllUserSessions)
			r.Post("/users/sessions/revoke/all", h.revokeAllSessionsGlobal)

			// Pillar C — Typing HTTP fallback for headless clients.
			r.Post("/users/{userID}/typing", h.postUserTyping)

			// Pillar D — Thread set_unread (rewinds last_viewed_at on
			// the thread to (anchor.create_at - 1)).
			r.Post("/users/{userID}/teams/{teamID}/threads/{rootID}/set_unread/{postID}", h.setThreadUnread)

			// Pillar E — Promote / demote (system_user ⇄ system_guest).
			// Admin-only; self-demotion is rejected to avoid lock-out.
			r.Post("/users/{userID}/promote", h.promoteUser)
			r.Post("/users/{userID}/demote", h.demoteUser)

			// Pillar F — Reminder URL alias + recent-custom-status stub.
			r.Post("/users/{userID}/posts/{postID}/reminder", h.createUserPostReminder)
			r.Post("/users/{userID}/status/custom/recent/delete", h.deleteRecentCustomStatus)

			// Phase 26 — Mattermost API v4 compatibility wave 6 (auth chain).
			//
			// Pillar D — Channel + group bulk hydrate. Both are
			// caller-scoped (results gated by membership / public
			// visibility), so they live in the regular auth chain
			// rather than the admin block.
			r.Post("/teams/{teamID}/channels/ids", h.channelsByIDsInTeam)
			r.Post("/users/group_channels", h.listMyGroupChannels)

			// Pillar B — Team admin restore + invite shape.
			// Both handlers do their own callerCanAdminTeam gate.
			r.Post("/teams/{teamID}/regenerate_invite_id", h.regenerateTeamInviteID)
			r.Post("/teams/{teamID}/invite/email", h.inviteTeamMembersByEmail)

			// Pillar C — Posts move + restore. Move requires
			// channel_admin on BOTH source and destination; restore
			// requires channel_admin on the post's channel. Handlers
			// run their own callerCanAdminChannel gate.
			r.Post("/posts/{postID}/move", h.movePost)
			r.Post("/posts/{postID}/restore/{revID}", h.restorePost)

			// Phase 27 — Mattermost API v4 compatibility wave 7
			// (auth chain).
			//
			// Pillar B — Posts compat. Action button dispatch is a
			// stub (no interactive integrations server-side yet);
			// ids/reactions hydrates a batch of posts' reactions
			// gated on per-post channel membership.
			r.Post("/posts/{postID}/actions/{actionID}", h.doPostAction)
			r.Post("/posts/ids/reactions", h.postsByIDsReactions)

			// Pillar E — Notifications ack + flagged posts. The ack
			// endpoint is a stub for mobile push receipts; the
			// flagged-posts route shells over the existing saved-
			// posts store (Mattermost's "flagged" concept maps 1:1
			// to our bookmarks).
			r.Post("/notifications/ack", h.ackNotification)
			r.Get("/users/{userID}/posts/flagged", h.listFlaggedPosts)

			// Phase 28 — interactive dialog compatibility. These are
			// auth-chain routes because slash commands and post actions
			// can open/submit dialogs on behalf of regular users.
			r.Post("/actions/dialogs/open", h.openDialog)
			r.Post("/actions/dialogs/lookup", h.lookupDialog)
			r.Post("/actions/dialogs/submit", h.submitDialog)

			// Phase 28 (wave 8) — Mattermost API v4 compatibility.
			// Five pillars covering post acks, terms of service, MFA,
			// bulk channel members, and admin-edge stubs. All zero-
			// schema; most are 200-OK stubs that exist so the official
			// client doesn't 404 on probe.
			//
			// Pillar A — Post acknowledgments. Three endpoints; storage
			// not yet modeled, so ack/unack audit and 200, list returns [].
			r.Post("/users/{userID}/posts/{postID}/ack", h.ackPost)
			r.Delete("/users/{userID}/posts/{postID}/ack", h.unackPost)
			r.Get("/posts/{postID}/acknowledgements", h.listPostAcknowledgements)

			// Pillar B — Terms of Service. Three endpoints with tiny
			// in-memory state; survives a hot path without needing a
			// schema change. Real durable enforcement lands later.
			r.Get("/terms_of_service", h.getTermsOfService)
			r.Post("/terms_of_service", h.updateTermsOfService)
			r.Get("/users/{userID}/terms_of_service", h.getUserTermsOfServiceStatus)
			r.Post("/users/{userID}/terms_of_service", h.acceptTermsOfService)

			// Pillar C — MFA stubs. Three endpoints; we don't ship MFA
			// yet but the official client probes these on the settings
			// page. checkUserMFA is anti-oracle (always returns
			// mfa_required:false regardless of whether the login_id
			// resolves to a real user).
			r.Post("/users/mfa", h.checkUserMFA)
			r.Put("/users/{userID}/mfa", h.setUserMFA)
			r.Post("/users/{userID}/mfa/generate", h.generateUserMFA)

			// Pillar D — Bulk channel member add. One endpoint; wraps
			// the existing single-user Join in a loop with per-member
			// error reporting.
			r.Put("/channels/{channelID}/members", h.bulkAddChannelMembers)

			// Pillar E — Admin-edge stubs. setUserAuthMethod is the
			// admin force-rotate auth provider stub; listKnownUsers is
			// the union of users sharing channels with the caller.
			r.Put("/users/{userID}/auth", h.setUserAuthMethod)
			r.Get("/users/known", h.listKnownUsers)

			// Phase 29 (wave 9) — Mattermost API v4 compatibility.
			// Five pillars: channel bookmarks (real feature, new table),
			// admin diagnostics stubs, hooks GET, team channel scopes,
			// and misc usage/redirect stubs.
			//
			// Pillar A — Channel bookmarks. Channel-scoped pinned URL
			// + file links above the message stream. Five endpoints
			// (list/create/patch/delete/reorder); soft-delete via the
			// service. Members can read+create+edit+reorder; only the
			// owner or a channel admin can delete.
			r.Get("/channels/{channelID}/bookmarks", h.listChannelBookmarks)
			r.Post("/channels/{channelID}/bookmarks", h.createChannelBookmark)
			r.Patch("/channels/{channelID}/bookmarks/{bookmarkID}", h.patchChannelBookmark)
			r.Delete("/channels/{channelID}/bookmarks/{bookmarkID}", h.deleteChannelBookmark)
			r.Post("/channels/{channelID}/bookmarks/{bookmarkID}/sort_order", h.reorderChannelBookmark)

			// Pillar C — Hooks GET. Admin-only single-hook lookup so
			// the integrations tab can pre-fill a patch form with the
			// row's current values. Webhook update endpoints from
			// Phase 24 expect callers to read first, then PUT.
			r.Get("/hooks/incoming/{hookID}", h.getIncomingHook)
			r.Get("/hooks/outgoing/{hookID}", h.getOutgoingHook)

			// Pillar D — Team channel scopes. Listings used by the
			// admin console + channel-picker overlays. Private + deleted
			// scopes; search_autocomplete is a URL-shape alias of the
			// Phase 21 POST /teams/{tid}/channels/search.
			r.Get("/teams/{teamID}/channels/private", h.listTeamPrivateChannels)
			r.Get("/teams/{teamID}/channels/deleted", h.listTeamDeletedChannels)
			r.Get("/teams/{teamID}/channels/search_autocomplete", h.searchAutocompleteTeamChannels)

			// Pillar E — Usage + redirect stubs. usage/posts and
			// usage/storage are real (cheap aggregate queries);
			// limits/server is a constants-only response (we don't
			// enforce caps); redirect_location echoes the URL (no
			// SSRF gateway); image is a thin wrapper over the existing
			// link-preview proxy.
			r.Get("/usage/posts", h.getUsagePosts)
			r.Get("/usage/storage", h.getUsageStorage)
			r.Get("/limits/server", h.getServerLimits)
			r.Get("/redirect_location", h.getRedirectLocation)
			r.Get("/image", h.getProxiedImage)

			// Pillar B — Admin diagnostics. The route shapes remain for
			// compatibility, but unsupported probes return 501 AppErrors
			// instead of false green-check successes.
			r.Group(func(r chi.Router) {
				r.Use(h.requireRole("system_admin"))
				r.Post("/email/test", h.adminTestEmail)
				r.Post("/notifications/test", h.adminTestNotifications)
				r.Post("/file/s3_test", h.adminTestS3)
				r.Post("/site_url/test", h.adminTestSiteURL)
				r.Post("/elasticsearch/test", h.adminTestElasticsearch)
				r.Post("/elasticsearch/purge_indexes", h.adminPurgeElasticsearch)
				r.Post("/bleve/purge_indexes", h.adminPurgeBleve)
				r.Post("/caches/invalidate", h.adminInvalidateCaches)
				r.Post("/database/recycle", h.adminRecycleDB)
				r.Post("/integrity", h.adminCheckIntegrity)
			})

			// Phase 30 (wave 10) — Mattermost API v4 compatibility.
			// Five pillars: team destructive aliases, channel views
			// stubs, channel admin, reports/admin stats stubs, login
			// fallbacks. All chosen from the back of the audit's
			// missing-endpoint list to stay clear of codex's compat
			// handler files.
			//
			// Pillar A — Team destructive aliases. Each handler does
			// its own role check; mixed-scope (DELETE /teams/invites/
			// email is global system_admin, kick-member is team_admin
			// or self-leave).
			r.Delete("/teams/{teamID}", h.deleteTeam)
			r.Delete("/teams/{teamID}/members/{userID}", h.removeTeamMember)
			r.Delete("/teams/{teamID}/image", h.deleteTeamImage)
			r.Post("/teams/{teamID}/image", h.uploadTeamImage)
			r.Get("/teams/{teamID}/image", h.getTeamImage)
			r.Delete("/teams/invites/email", h.revokeTeamEmailInvites)

			// Pillar B — Channel views stubs. Saved-views feature
			// doesn't have storage yet; endpoints exist so the
			// official client's "Saved views" UI doesn't 404. Member-
			// gated reads, owner-or-admin writes (handlers enforce).
			r.Get("/channels/{channelID}/views", h.listChannelViews)
			r.Post("/channels/{channelID}/views", h.createChannelView)
			r.Get("/channels/{channelID}/views/{viewID}", h.getChannelView)
			r.Patch("/channels/{channelID}/views/{viewID}", h.patchChannelView)
			r.Delete("/channels/{channelID}/views/{viewID}", h.deleteChannelView)
			r.Get("/channels/{channelID}/views/{viewID}/posts", h.listChannelViewPosts)
			r.Post("/channels/{channelID}/views/{viewID}/sort_order", h.reorderChannelView)

			// Pillar C — Channel admin operations. Mix of real-ish
			// (timezones, by-name lookup) and stubs (moderations,
			// scheme, move). All handlers do their own gates.
			r.Get("/channels/{channelID}/timezones", h.listChannelTimezones)
			r.Get("/channels/{channelID}/moderations", h.getChannelModerations)
			r.Put("/channels/{channelID}/moderations/patch", h.patchChannelModerations)
			r.Put("/channels/{channelID}/scheme", h.setChannelScheme)
			r.Post("/channels/{channelID}/move", h.moveChannel)
			r.Get("/teams/name/{teamName}/channels/name/{channelName}", h.getTeamChannelByName)

			// Pillar E — Login fallbacks. login_type is anti-oracle
			// (always returns "email"); the rest are admin-self
			// stubs.
			r.Post("/users/login/switch", h.loginSwitch)
			r.Post("/users/login/type", h.loginType)
			r.Post("/users/login/cws", h.loginCWS)
			r.Post("/users/login/sso/code-exchange", h.loginSSOCodeExchange)
			r.Post("/users/{userID}/email/verify/member", h.adminVerifyMemberEmail)

			// Pillar D — Reports + admin stats stubs (admin-only).
			r.Group(func(r chi.Router) {
				r.Use(h.requireRole("system_admin"))
				r.Get("/users/invalid_emails", h.getInvalidEmails)
				r.Get("/users/stats/filtered", h.getFilteredUserStats)
				r.Get("/reports/users", h.getReportsUsers)
				r.Get("/reports/users/count", h.getReportsUsersCount)
				r.Post("/reports/users/export", h.exportReportsUsers)
				r.Post("/reports/posts", h.reportPosts)
				r.Get("/channels", h.listAllChannels)
			})

			// Phase 31 (wave 11) — Mattermost API v4 compatibility.
			// Six pillars: bot icons, thread expansions, files
			// search/info, uploads multipart stubs, channel group +
			// posts unread, group-aware listings + misc admin.
			//
			// Pillar A — Bot icons (3 endpoints, stubs — no storage).
			r.Get("/bots/{botUserID}/icon", h.getBotIcon)
			r.Post("/bots/{botUserID}/icon", h.uploadBotIcon)
			r.Delete("/bots/{botUserID}/icon", h.deleteBotIcon)

			// Pillar B — Thread expansions (3 endpoints — DELETE
			// follow + GET membership/mention-counts). Self/admin
			// gated by handlers.
			r.Delete("/users/{userID}/teams/{teamID}/threads/{threadID}/following", h.unfollowThread)
			r.Get("/users/{userID}/teams/{teamID}/threads/{threadID}", h.getThreadMembership)
			r.Get("/users/{userID}/teams/{teamID}/threads/mention_counts", h.getThreadMentionCounts)

			// Pillar C — Files (3 endpoints, mix real + stub).
			r.Get("/posts/{postID}/files/info", h.getPostFilesInfo)
			r.Post("/files/search", h.searchFiles)
			r.Post("/teams/{teamID}/files/search", h.searchTeamFiles)

			// Pillar D — Uploads multipart stubs (4 endpoints — we
			// don't model resumable upload sessions yet).
			r.Post("/uploads", h.initUploadSession)
			r.Get("/uploads/{uploadID}", h.getUploadSession)
			r.Post("/uploads/{uploadID}", h.uploadChunk)
			r.Get("/users/{userID}/uploads", h.listUserUploads)

			// Pillar E — Channels group + posts unread (3 endpoints
			// — group create real via EnsureGroup, posts/unread
			// real, search stub).
			r.Post("/channels/group", h.createGroupChannel)
			r.Post("/channels/group/search", h.searchGroupChannels)
			r.Get("/users/{userID}/channels/{channelID}/posts/unread", h.getChannelPostsUnread)

			// Pillar F — Group-aware listings + misc admin (7
			// endpoints — most are stubs returning empty/zero
			// since we don't model LDAP groups).
			r.Get("/channels/{channelID}/member_counts_by_group", h.getChannelMemberCountsByGroup)
			r.Get("/channels/{channelID}/members_minus_group_members", h.getChannelMembersMinusGroup)
			r.Get("/teams/{teamID}/members_minus_group_members", h.getTeamMembersMinusGroup)
			r.Delete("/users/status/custom/recent", h.clearRecentCustomStatuses)
			r.Delete("/users/{userID}/status/custom/recent", h.clearRecentCustomStatuses)
			r.Post("/teams/{teamID}/invite-guests/email", h.inviteGuestsByEmail)
			r.Put("/channels/{channelID}/members/{userID}/autotranslation", h.setMemberAutotranslation)
			r.Group(func(r chi.Router) {
				r.Use(h.requireRole("system_admin"))
				r.Post("/users/bulk_delete", h.bulkDeleteUsers)
				r.Delete("/users", h.bulkDeleteUsers)
			})

			// Phase 32 (wave 12) — Mattermost API v4 compatibility.
			// Six pillars: A (audit-regex restructure of Phase 27 routes),
			// B (custom profile attributes — only real schema work in
			// Phase 32), C (recaps stubs), D (AI/agents stubs), E (OAuth
			// apps + outgoing connections stubs), F (cloud + IP filter +
			// imports/exports + misc admin stubs).

			// Pillar B — Custom profile attributes (7 endpoints — real
			// schema, real CRUD against custom_profile_fields +
			// custom_profile_values tables).
			r.Get("/custom_profile_attributes/fields", h.listCustomProfileFields)
			r.Patch("/custom_profile_attributes/values", h.patchCustomProfileValuesGlobal)
			r.Get("/users/{userID}/custom_profile_attributes", h.getUserCustomProfileValues)
			r.Patch("/users/{userID}/custom_profile_attributes", h.patchUserCustomProfileValues)

			// Pillar C — Recaps (6 endpoints — in-memory LRU stubs).
			// Real impl would persist recap rows; current stubs satisfy
			// the official client's "AI recap" UI without 404s.
			r.Get("/recaps", h.listRecaps)
			r.Get("/recaps/{recapID}", h.getRecap)
			r.Post("/recaps", h.createRecap)
			r.Delete("/recaps/{recapID}", h.deleteRecap)
			r.Post("/recaps/{recapID}/read", h.markRecapRead)
			r.Post("/recaps/{recapID}/regenerate", h.regenerateRecap)

			// Pillar E — OAuth apps caller-facing routes (3 endpoints).
			// /info strips the secret; /authorized is per-user; /register
			// is a stub for the self-serve "register an app" flow.
			r.Get("/oauth/apps/{appID}/info", h.getOAuthAppInfo)
			r.Get("/users/{userID}/oauth/apps/authorized", h.listAuthorizedOAuthApps)
			r.Post("/oauth/apps/register", h.registerOAuthApp)

			// Pillar F — Posts burn/reveal/rewrite (3 endpoints + 3
			// official-shape aliases — stubs; real burn would do a
			// delayed delete + audit, real rewrite would route through
			// an LLM).
			r.Post("/posts/{postID}/burn", h.burnPost)
			r.Delete("/posts/{postID}/burn", h.burnPost)
			r.Get("/posts/{postID}/reveal", h.revealPost)
			r.Post("/posts/{postID}/rewrite", h.rewritePost)
			r.Post("/posts/rewrite", h.rewritePost)

			// Pillar F — Misc auth-chain stubs (channel/team-scoped).
			r.Get("/users/{userID}/channels/managed_categories", h.listManagedCategories)
			r.Get("/teams/{teamID}/channels/managed_categories", h.listManagedCategories)
			r.Get("/channels/{channelID}/access_control/attributes", h.channelAccessControlAttrs)
			r.Get("/channels/{channelID}/common_teams", h.channelCommonTeams)
			r.Post("/channels/{channelID}/common_teams", h.channelCommonTeams)
			r.Post("/client_perf", h.postClientPerf)
			r.Get("/permissions/ancillary", h.postPermissionsAncillary)
			r.Post("/permissions/ancillary", h.postPermissionsAncillary)
			r.Post("/teams/{teamID}/invite/email", h.inviteTeamMembersFromBody)
			r.Post("/teams/members/invite", h.inviteTeamMembersFromBody)
			r.Get("/custom_profile_attributes/group", h.listCustomProfileFields)

			// Pillar F admin sub-group — system_admin-only stubs.
			r.Group(func(r chi.Router) {
				r.Use(h.requireRole("system_admin"))

				// Pillar B admin paths — field create/patch/delete.
				r.Post("/custom_profile_attributes/fields", h.createCustomProfileField)
				r.Patch("/custom_profile_attributes/fields/{fieldID}", h.patchCustomProfileField)
				r.Delete("/custom_profile_attributes/fields/{fieldID}", h.deleteCustomProfileField)

				// Pillar D — AI/Agents/LLM (5 endpoints).
				r.Get("/agents", h.listAIAgents)
				r.Get("/agents/status", h.getAIAgentsStatus)
				r.Get("/ai/agents", h.listAIAgentsAlt)
				r.Get("/ai/services", h.listAIServices)
				r.Get("/llm/services", h.listLLMServices)
				r.Get("/llmservices", h.listLLMServices)

				// Pillar E — OAuth apps admin (5 endpoints) +
				// outgoing connections (6 endpoints).
				r.Get("/oauth/apps", h.listOAuthApps)
				r.Post("/oauth/apps", h.createOAuthApp)
				r.Get("/oauth/apps/{appID}", h.getOAuthApp)
				r.Put("/oauth/apps/{appID}", h.updateOAuthApp)
				r.Delete("/oauth/apps/{appID}", h.deleteOAuthApp)
				r.Post("/oauth/apps/{appID}/regen_secret", h.regenOAuthAppSecret)
				r.Get("/oauth/outgoing_connections", h.listOAuthOutgoing)
				r.Post("/oauth/outgoing_connections", h.createOAuthOutgoing)
				r.Get("/oauth/outgoing_connections/{connectionID}", h.getOAuthOutgoing)
				r.Put("/oauth/outgoing_connections/{connectionID}", h.updateOAuthOutgoing)
				r.Delete("/oauth/outgoing_connections/{connectionID}", h.deleteOAuthOutgoing)
				r.Post("/oauth/outgoing_connections/validate", h.validateOAuthOutgoing)

				// Pillar F — Cloud billing (14 endpoints).
				r.Get("/cloud/check-cws-connection", h.cloudCheckCWS)
				r.Get("/cloud/customer", h.cloudGetCustomer)
				r.Put("/cloud/customer", h.cloudPutCustomer)
				r.Put("/cloud/customer/address", h.cloudPutCustomerAddress)
				r.Get("/cloud/installation", h.cloudGetInstallation)
				r.Get("/cloud/limits", h.cloudGetLimits)
				r.Get("/cloud/preview-modal-data", h.cloudGetPreviewModalData)
				r.Get("/cloud/preview/modal_data", h.cloudGetPreviewModalData)
				r.Get("/cloud/products", h.cloudGetProducts)
				r.Get("/cloud/subscription", h.cloudGetSubscription)
				r.Get("/cloud/subscription/invoices", h.cloudGetSubscriptionInvoices)
				r.Get("/cloud/subscription/invoices/{invoiceID}/pdf", h.cloudGetSubscriptionInvoicePDF)
				r.Post("/cloud/payment", h.cloudPostPayment)
				r.Post("/cloud/payment/confirm", h.cloudConfirmPayment)
				r.Post("/cloud/webhook", h.cloudWebhook)

				// Pillar F — IP filtering (3 endpoints).
				r.Get("/ip_filtering", h.listIPFiltering)
				r.Get("/ip_filtering/my_ip", h.getMyIP)
				r.Post("/ip_filtering", h.saveIPFiltering)

				// Pillar F — Imports/Exports (5 endpoints).
				r.Get("/imports", h.listImports)
				r.Delete("/imports/{importName}", h.deleteImport)
				r.Get("/exports", h.listExports)
				r.Get("/exports/{exportName}", h.getExport)
				r.Delete("/exports/{exportName}", h.deleteExport)

				// Pillar F — Misc admin (8 endpoints).
				r.Post("/restart", h.restartServer)
				r.Post("/teams/{teamID}/import", h.postTeamImport)
				r.Put("/teams/{teamID}/scheme", h.putTeamScheme)
				r.Get("/analytics/old", h.analyticsOld)
				r.Post("/users/{userID}/image", h.adminUploadUserImage)
			})

			r.Post("/commands/execute", h.executeCommand)

			r.Get("/plugins/statuses", h.listPluginStatuses)
			r.Get("/plugins/webapp", h.listPluginWebapp)
			r.Get("/plugins/marketplace", h.listPluginMarketplace)

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
				r.Get("/config/environment", h.getEnvironmentConfig)
				r.Get("/config", h.getConfig)
				r.Put("/config", h.putConfig)
				r.Put("/config/patch", h.patchConfig)
				r.Post("/config/reload", h.reloadConfig)
				r.Get("/plugins", h.listPlugins)
				r.Get("/audit/logs", h.listAudit)
				r.Get("/audits", h.listAudit)
				r.Get("/users/{userID}/audits", h.listUserAudits)
				r.Get("/logs", h.getLogs)
				r.Post("/logs", h.postLog)
				r.Get("/logs/download", h.downloadLogs)
				r.Get("/cluster/status", h.getClusterStatus)
				r.Get("/server_busy", h.getServerBusy)
				r.Post("/server_busy", h.setServerBusy)
				r.Delete("/server_busy", h.clearServerBusy)
				r.Get("/roles", h.listRoles)
				r.Get("/roles/{roleID}", h.getRole)
				r.Get("/roles/name/{name}", h.getRoleByName)
				r.Post("/roles/names", h.getRolesByNames)
				r.Put("/roles/{roleID}/patch", h.patchRole)
				r.Get("/jobs", h.listJobs)
				r.Post("/jobs", h.createJob)
				r.Get("/jobs/{jobID}", h.getJob)
				r.Patch("/jobs/{jobID}/status", h.patchJobStatus)
				r.Post("/jobs/{jobID}/cancel", h.cancelJob)
				r.Get("/jobs/{jobID}/download", h.downloadJob)
				r.Get("/jobs/type/{jobType}", h.listJobsByType)
				r.Post("/plugins", h.uploadPlugin)
				r.Delete("/plugins/{pluginID}", h.deletePlugin)
				r.Post("/plugins/{pluginID}/enable", h.enablePlugin)
				r.Post("/plugins/{pluginID}/disable", h.disablePlugin)
				r.Post("/plugins/install_from_url", h.installPluginFromURL)
				r.Post("/plugins/marketplace", h.installPluginFromMarketplace)
				r.Get("/plugins/marketplace/first_admin_visit", h.getMarketplaceFirstAdminVisit)
				r.Post("/plugins/marketplace/first_admin_visit", h.saveMarketplaceFirstAdminVisit)
				r.Get("/license/load_metric", h.getLicenseLoadMetric)
				r.Get("/license/renewal", h.getLicenseRenewal)
				r.Post("/license", h.uploadLicense)
				r.Delete("/license", h.deleteLicense)
				r.Get("/trial-license/prev", h.getPreviousTrialLicense)
				r.Post("/trial-license", h.requestTrialLicense)
				r.Get("/upgrade_to_enterprise/allowed", h.getUpgradeToEnterpriseAllowed)
				r.Get("/upgrade_to_enterprise/status", h.getUpgradeToEnterpriseStatus)
				r.Post("/upgrade_to_enterprise", h.upgradeToEnterprise)
				r.Get("/brand/image", h.getBrandImage)
				r.Post("/brand/image", h.uploadBrandImage)
				r.Delete("/brand/image", h.deleteBrandImage)
				r.Get("/ldap/groups", h.listLDAPGroups)
				r.Post("/ldap/groups/{groupID}/link", h.linkLDAPGroup)
				r.Delete("/ldap/groups/{groupID}/link", h.unlinkLDAPGroup)
				r.Post("/ldap/certificate/private", h.uploadLDAPCertificate)
				r.Post("/ldap/certificate/public", h.uploadLDAPCertificate)
				r.Delete("/ldap/certificate/private", h.deleteLDAPCertificate)
				r.Delete("/ldap/certificate/public", h.deleteLDAPCertificate)
				r.Post("/ldap/migrateid", h.ldapDisabledOK)
				r.Post("/ldap/sync", h.ldapDisabledOK)
				r.Post("/ldap/test", h.ldapDisabledOK)
				r.Post("/ldap/test_connection", h.ldapDisabledOK)
				r.Post("/ldap/test_diagnostics", h.ldapDisabledOK)
				r.Post("/ldap/users/{userID}/group_sync_memberships", h.ldapDisabledOK)
				r.Post("/users/migrate_auth/ldap", h.migrateAuthLDAP)
				r.Get("/saml/certificate/status", h.getSAMLCertificateStatus)
				r.Get("/saml/metadata", h.getSAMLMetadata)
				r.Post("/saml/metadatafromidp", h.uploadSAMLMetadataFromIDP)
				r.Post("/saml/reset_auth_data", h.resetSAMLAuthData)
				r.Post("/saml/certificate/idp", h.uploadSAMLCertificate)
				r.Post("/saml/certificate/private", h.uploadSAMLCertificate)
				r.Post("/saml/certificate/public", h.uploadSAMLCertificate)
				r.Delete("/saml/certificate/idp", h.deleteSAMLCertificate)
				r.Delete("/saml/certificate/private", h.deleteSAMLCertificate)
				r.Delete("/saml/certificate/public", h.deleteSAMLCertificate)
				r.Get("/content_flagging/config", h.getContentFlaggingConfig)
				r.Put("/content_flagging/config", h.putContentFlaggingConfig)
				r.Get("/content_flagging/flag/config", h.getContentFlaggingFlagConfig)
				r.Get("/system/e2e/ai_bridge", h.getAIBridge)
				r.Put("/system/e2e/ai_bridge", h.putAIBridge)
				r.Delete("/system/e2e/ai_bridge", h.deleteAIBridge)
				r.Get("/system/notices/{noticeID}", h.getSystemNotice)
				r.Put("/system/notices/view", h.markSystemNoticeViewed)
				r.Get("/system/support_packet", h.getSupportPacket)
				r.Get("/access_control_policies/cel/autocomplete/fields", h.getAccessControlCELFields)
				r.Post("/access_control_policies/cel/check", h.checkAccessControlCEL)
				r.Post("/access_control_policies/cel/test", h.testAccessControlCEL)
				r.Post("/access_control_policies/cel/validate_requester", h.validateAccessControlRequester)
				r.Post("/access_control_policies/cel/visual_ast", h.visualAccessControlAST)
				r.Post("/access_control_policies/search", h.searchAccessControlPolicies)
				r.Put("/access_control_policies", h.upsertAccessControlPolicy)
				r.Put("/access_control_policies/activate", h.activateAccessControlPolicies)
				r.Get("/access_control_policies/{policyID}", h.getAccessControlPolicy)
				r.Delete("/access_control_policies/{policyID}", h.deleteAccessControlPolicy)
				r.Get("/access_control_policies/{policyID}/activate", h.getAccessControlPolicyActivation)
				r.Post("/access_control_policies/{policyID}/assign", h.assignAccessControlPolicy)
				r.Delete("/access_control_policies/{policyID}/unassign", h.unassignAccessControlPolicy)
				r.Get("/access_control_policies/{policyID}/resources/channels", h.listAccessControlPolicyChannels)
				r.Post("/access_control_policies/{policyID}/resources/channels/search", h.searchAccessControlPolicyChannels)

				r.Post("/audit_logs/certificate", h.uploadAuditLogCertificate)
				r.Delete("/audit_logs/certificate", h.deleteAuditLogCertificate)
				r.Get("/compliance/reports", h.listComplianceReports)
				r.Post("/compliance/reports", h.createComplianceReport)
				r.Get("/compliance/reports/{reportID}", h.getComplianceReport)
				r.Get("/compliance/reports/{reportID}/download", h.downloadComplianceReport)
				r.Get("/content_flagging/fields", h.getContentFlaggingFields)
				r.Get("/content_flagging/post/{postID}", h.getContentFlaggingPost)
				r.Get("/content_flagging/post/{postID}/field_values", h.getContentFlaggingPostFieldValues)
				r.Get("/content_flagging/team/{teamID}/reviewers/search", h.searchContentFlaggingTeamReviewers)
				r.Get("/content_flagging/team/{teamID}/status", h.getContentFlaggingTeamStatus)
				r.Post("/content_flagging/post/{postID}/assign/{userID}", h.assignContentFlaggedPost)
				r.Post("/content_flagging/post/{postID}/flag", h.flagContentPost)
				r.Put("/content_flagging/post/{postID}/keep", h.keepContentFlaggedPost)
				r.Put("/content_flagging/post/{postID}/remove", h.removeContentFlaggedPost)
				r.Get("/data_retention/policy", h.getDataRetentionPolicy)
				r.Get("/data_retention/policies", h.listDataRetentionPolicies)
				r.Post("/data_retention/policies", h.createDataRetentionPolicy)
				r.Get("/data_retention/policies_count", h.getDataRetentionPoliciesCount)
				r.Get("/data_retention/policies/{policyID}", h.getDataRetentionPolicyByID)
				r.Patch("/data_retention/policies/{policyID}", h.patchDataRetentionPolicy)
				r.Delete("/data_retention/policies/{policyID}", h.deleteDataRetentionPolicy)
				r.Get("/data_retention/policies/{policyID}/channels", h.compatEmptyList)
				r.Post("/data_retention/policies/{policyID}/channels", h.compatOK)
				r.Post("/data_retention/policies/{policyID}/channels/search", h.compatEmptyList)
				r.Delete("/data_retention/policies/{policyID}/channels", h.compatOK)
				r.Get("/data_retention/policies/{policyID}/teams", h.compatEmptyList)
				r.Post("/data_retention/policies/{policyID}/teams", h.compatOK)
				r.Post("/data_retention/policies/{policyID}/teams/search", h.compatEmptyList)
				r.Delete("/data_retention/policies/{policyID}/teams", h.compatOK)
				r.Get("/users/{userID}/data_retention/channel_policies", h.compatEmptyList)
				r.Get("/users/{userID}/data_retention/team_policies", h.compatEmptyList)
				r.Get("/groups", h.listGroups)
				r.Post("/groups", h.createGroup)
				r.Post("/groups/names", h.getGroupsByNames)
				r.Get("/groups/{groupID}", h.getGroup)
				r.Delete("/groups/{groupID}", h.compatOK)
				r.Put("/groups/{groupID}/patch", h.patchGroup)
				r.Post("/groups/{groupID}/restore", h.restoreGroup)
				r.Get("/groups/{groupID}/stats", h.getGroupStats)
				r.Get("/groups/{groupID}/members", h.compatEmptyList)
				r.Post("/groups/{groupID}/members", h.compatOK)
				r.Delete("/groups/{groupID}/members", h.compatOK)
				r.Get("/groups/{groupID}/channels", h.compatEmptyList)
				r.Get("/groups/{groupID}/channels/{channelID}", h.compatEmptyObject)
				r.Post("/groups/{groupID}/channels/{channelID}/link", h.compatOK)
				r.Delete("/groups/{groupID}/channels/{channelID}/link", h.compatOK)
				r.Put("/groups/{groupID}/channels/{channelID}/patch", h.compatEmptyObject)
				r.Get("/groups/{groupID}/teams", h.compatEmptyList)
				r.Get("/groups/{groupID}/teams/{teamID}", h.compatEmptyObject)
				r.Post("/groups/{groupID}/teams/{teamID}/link", h.compatOK)
				r.Delete("/groups/{groupID}/teams/{teamID}/link", h.compatOK)
				r.Put("/groups/{groupID}/teams/{teamID}/patch", h.compatEmptyObject)
				r.Get("/channels/{channelID}/groups", h.compatEmptyList)
				r.Get("/teams/{teamID}/groups", h.compatEmptyList)
				r.Get("/users/{userID}/groups", h.compatEmptyList)
				r.Get("/schemes", h.listSchemes)
				r.Post("/schemes", h.createScheme)
				r.Get("/schemes/{schemeID}", h.getScheme)
				r.Put("/schemes/{schemeID}/patch", h.patchScheme)
				r.Delete("/schemes/{schemeID}", h.compatOK)
				r.Get("/schemes/{schemeID}/channels", h.compatEmptyList)
				r.Get("/schemes/{schemeID}/teams", h.compatEmptyList)
				r.Get("/remotecluster", h.listRemoteClusters)
				r.Post("/remotecluster", h.createRemoteCluster)
				r.Get("/remotecluster/{remoteID}", h.getRemoteCluster)
				r.Patch("/remotecluster/{remoteID}", h.patchRemoteCluster)
				r.Delete("/remotecluster/{remoteID}", h.compatOK)
				r.Get("/remotecluster/{remoteID}/sharedchannelremotes", h.compatEmptyList)
				r.Post("/remotecluster/{remoteID}/generate_invite", h.generateRemoteClusterInvite)
				r.Post("/remotecluster/accept_invite", h.acceptRemoteClusterInvite)
				r.Post("/remotecluster/{remoteID}/channels/{channelID}/invite", h.remoteClusterChannelInvite)
				r.Post("/remotecluster/{remoteID}/channels/{channelID}/uninvite", h.compatOK)
				r.Get("/sharedchannels/{channelID}", h.getSharedChannel)
				r.Get("/sharedchannels/{channelID}/remotes", h.compatEmptyList)
				r.Get("/sharedchannels/remote_info/{remoteID}", h.getSharedChannelRemoteInfo)
				r.Get("/sharedchannels/users/{userID}/can_dm/{otherUserID}", h.canDMSharedChannelUser)
				r.Get("/properties/groups/{groupID}/{targetID}/fields", h.compatEmptyList)
				r.Post("/properties/groups/{groupID}/{targetID}/fields", h.createPropertyField)
				r.Patch("/properties/groups/{groupID}/{targetID}/fields/{fieldID}", h.patchPropertyField)
				r.Delete("/properties/groups/{groupID}/{targetID}/fields/{fieldID}", h.compatOK)
				r.Get("/properties/groups/{groupID}/{targetID}/values/{fieldID}", h.getPropertyValue)
				r.Patch("/properties/groups/{groupID}/{targetID}/values/{fieldID}", h.patchPropertyValue)

				// Phase 16 admin actions
				r.Post("/users/{userID}/reactivate", h.reactivateUser)
				r.Post("/channels/{channelID}/archive", h.archiveChannel)
				r.Post("/channels/{channelID}/restore", h.restoreChannel)
				r.Delete("/channels/{channelID}", h.deleteChannel)
				r.Post("/channels/search", h.searchChannelsAll)

				// Bot CRUD
				r.Post("/bots", h.createBot)
				r.Get("/bots", h.listBots)
				r.Delete("/bots/{botID}", h.disableBot)
				r.Post("/tokens/{tokenID}/revoke", h.revokeToken)

				// Incoming webhook CRUD (the fire endpoint is public,
				// mounted above).
				r.Post("/hooks/incoming", h.createIncomingWebhook)
				r.Get("/hooks/incoming", h.listIncomingWebhooks)
				r.Put("/hooks/incoming/{hookID}", h.updateIncomingWebhook)
				r.Delete("/hooks/incoming/{hookID}", h.deleteIncomingWebhook)

				// Outgoing webhook CRUD
				r.Post("/hooks/outgoing", h.createOutgoingWebhook)
				r.Get("/hooks/outgoing", h.listOutgoingWebhooks)
				r.Put("/hooks/outgoing/{hookID}", h.updateOutgoingWebhook)
				r.Delete("/hooks/outgoing/{hookID}", h.deleteOutgoingWebhook)

				// Bot update (admin-only). Bot create/disable/list already
				// gated above; PUT /bots/{id} mirrors Mattermost's contract.
				r.Put("/bots/{botID}", h.updateBot)

				// Custom slash command CRUD.
				r.Post("/commands", h.createCommand)
				r.Get("/commands", h.listCommandsForTeam)
				r.Get("/commands/{commandID}", h.getCommand)
				r.Put("/commands/{commandID}", h.updateCommand)
				r.Delete("/commands/{commandID}", h.deleteCommand)
				r.Put("/commands/{commandID}/regen_token", h.regenCommandToken)
				r.Put("/commands/{commandID}/move", h.moveCommand)

				// Phase 26 — Mattermost API v4 compatibility wave 6
				// (admin-only block).
				//
				// Pillar A — PAT operator surface. Admin-only because
				// disable/enable/revoke/search across ALL tokens is an
				// ops surface; per-user self-issue lives outside this
				// block above as /users/{userID}/tokens.
				r.Post("/users/tokens/disable", h.disableUserToken)
				r.Post("/users/tokens/enable", h.enableUserToken)
				r.Post("/users/tokens/revoke", h.revokeUserTokenByBody)
				r.Post("/users/tokens/search", h.searchUserTokens)

				// Pillar B — Team restore. Mirrors channel restore
				// already in this admin block.
				r.Post("/teams/{teamID}/restore", h.restoreTeam)

				// Pillar E — Convert user to bot + reset failed login
				// attempts. The reset endpoint is a stub (we don't
				// track failed attempts) but exists for client parity.
				r.Post("/users/{userID}/convert_to_bot", h.convertUserToBot)
				r.Post("/users/{userID}/reset_failed_attempts", h.resetUserFailedAttempts)

				// Pillar F — Outgoing webhook token rotation.
				r.Post("/hooks/outgoing/{hookID}/regen_token", h.regenerateOutgoingWebhookToken)

				// Phase 27 — Mattermost API v4 compatibility wave 7
				// (admin-only block).
				//
				// Pillar A — Bot lifecycle. Existing
				// `DELETE /bots/{id}` handles soft-delete; this block
				// adds the POST-shaped aliases the official admin
				// console uses, plus enable / convert_to_user / assign
				// / single-id GET.
				r.Get("/bots/{botID}", h.getBot)
				r.Post("/bots/{botID}/disable", h.disableBotByPost)
				r.Post("/bots/{botID}/enable", h.enableBot)
				r.Post("/bots/{botID}/convert_to_user", h.convertBotToUser)
				r.Post("/bots/{botID}/assign/{userID}", h.assignBotOwner)

				// Pillar B (admin slice) — Ephemeral post authoring.
				// Admin-only because letting any user spawn a fake
				// post visible only to one peer is a phishing vector.
				r.Post("/posts/ephemeral", h.createEphemeralPost)

				// Pillar D — User tokens GET. Admin-only because
				// listing every PAT in the system or peeking at a
				// stranger's token row is purely an ops surface.
				r.Get("/users/tokens", h.listAllUserTokens)
				r.Get("/users/tokens/{tokenID}", h.getUserToken)
			})
		})
	})

	// Moyro-native product surface. It intentionally remains separate from
	// the Mattermost compatibility boundary. Long-running AI and MCP streams
	// do not inherit the generic 30-second REST timeout.
	r.Route("/api/moyro/v1", func(r chi.Router) {
		r.Get("/system/info", h.nativeSystemInfo)
		r.With(oauthIPLimiter.Middleware(ratelimit.ClientIP), middleware.Timeout(30*time.Second)).
			Get("/auth/oidc/login", h.nativeOIDCLogin)
		r.With(oauthIPLimiter.Middleware(ratelimit.ClientIP), middleware.Timeout(30*time.Second)).
			Get("/auth/oidc/callback", h.nativeOIDCCallback)

		r.Group(func(r chi.Router) {
			r.Use(nativeBearerOnly)
			r.Use(h.nativeAPIKeyMiddleware)
			r.Use(patMW)
			r.Use(h.requireAuth)

			// Streaming uses the provider's administrator-configured timeout
			// and propagates browser cancellation to the upstream request.
			r.With(h.nativeRequire("use_ai")).Post("/me/ai/completions", h.nativeAICompletion)

			r.With(middleware.Timeout(30 * time.Second)).Group(func(r chi.Router) {
				r.Get("/me/permissions", h.getNativeEffectivePermissions)
				r.With(h.nativeRequire("manage_own_api_keys")).Get("/me/api-keys", h.listPersonalAPIKeys)
				r.With(h.nativeRequire("manage_own_api_keys")).Post("/me/api-keys", h.createPersonalAPIKey)
				r.With(h.nativeRequire("manage_own_api_keys")).Patch("/me/api-keys/{keyID}", h.patchPersonalAPIKey)
				r.With(h.nativeRequire("manage_own_api_keys")).Delete("/me/api-keys/{keyID}", h.revokePersonalAPIKey)
				r.With(h.nativeRequire("manage_own_api_keys")).Post("/me/api-keys/{keyID}/rotate", h.rotatePersonalAPIKey)
				r.With(h.nativeRequire("use_ai")).Get("/me/ai-preferences", h.getPersonalAIPreferences)
				r.With(h.nativeRequire("use_ai")).Patch("/me/ai-preferences", h.patchPersonalAIPreferences)
				r.With(h.nativeRequire("request_approval")).Post("/me/approval-requests", h.submitNativeApproval)
				r.Get("/me/approval-requests", h.listMyApprovalRequests)
				// Review permission is team-scoped. The list and decision handlers
				// resolve each request's team/channel before authorizing it; a
				// scope-less middleware check would incorrectly reject team_lead.
				r.Get("/reviews/approval-requests", h.listReviewApprovalRequests)
				r.Post("/reviews/approval-requests/{requestID}/decision", h.decideNativeApproval)

				r.With(h.nativeRequireSettingsSection).Get("/admin/settings/{section}", h.getNativeSettings)
				r.With(h.nativeRequireSettingsSection).Patch("/admin/settings/{section}", h.patchNativeSettings)
				r.With(h.nativeRequire("manage_oidc")).Get("/admin/oidc/providers", h.listNativeOIDCProviders)
				r.With(h.nativeRequire("manage_oidc")).Post("/admin/oidc/providers", h.saveNativeOIDCProvider)
				r.With(h.nativeRequire("manage_oidc")).Patch("/admin/oidc/providers/{providerID}", h.saveNativeOIDCProvider)
				r.With(h.nativeRequire("manage_oidc")).Post("/admin/oidc/providers/test", h.testNativeOIDCProvider)
				r.With(h.nativeRequire("manage_ai")).Get("/admin/ai/providers", h.listNativeAIProviders)
				r.With(h.nativeRequire("manage_ai")).Post("/admin/ai/providers", h.saveNativeAIProvider)
				r.With(h.nativeRequire("manage_ai")).Patch("/admin/ai/providers/{providerID}", h.saveNativeAIProvider)
				r.With(h.nativeRequire("manage_ai")).Post("/admin/ai/providers/test", h.testNativeAIProvider)
				r.With(h.nativeRequire("manage_approval_policies")).Get("/admin/approval-policies", h.listNativeApprovalPolicies)
				r.With(h.nativeRequire("manage_approval_policies")).Post("/admin/approval-policies", h.saveNativeApprovalPolicy)
				r.With(h.nativeRequire("manage_approval_policies")).Patch("/admin/approval-policies/{policyID}", h.saveNativeApprovalPolicy)
				r.With(h.nativeRequire("manage_roles")).Get("/admin/permissions", h.listNativePermissions)
				r.With(h.nativeRequire("manage_roles")).Get("/admin/roles", h.listNativeRoles)
				r.With(h.nativeRequire("manage_roles")).Patch("/admin/roles/{roleID}", h.patchNativeRole)
				r.With(h.nativeRequire("manage_api_keys")).Get("/admin/api-keys", h.listAdminAPIKeys)
				r.With(h.nativeRequire("manage_api_keys")).Delete("/admin/api-keys/{keyID}", h.revokeAdminAPIKey)
			})
		})
	})

	if h.native != nil && h.native.mcp != nil {
		r.With(nativeBearerOnly, h.nativeAPIKeyMiddleware, h.requireAuth, h.nativeMCPGate).Handle("/mcp", h.native.mcp.Handler())
	} else {
		r.HandleFunc("/mcp", func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusServiceUnavailable, "api.moyro.mcp.unavailable", "MCP service is unavailable")
		})
	}

	r.HandleFunc("/api/v4/websocket", h.websocket)
	if ui, err := webui.New(webui.DefaultRoot); err != nil {
		logger.Warn("production web UI unavailable", "root", webui.DefaultRoot, "err", err)
	} else {
		r.NotFound(ui.ServeHTTP)
	}

	scheduledWorker := scheduled.NewWorker(scheduledSvc, postSvc, fileSvc, hub, logger)
	remindersWorker := reminders.NewWorker(reminderSvc, postSvc, hub, logger)

	return &Backend{Router: r, Scheduled: scheduledWorker, Reminders: remindersWorker, Approvals: newApprovalExecutor(h.native, logger)}
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
