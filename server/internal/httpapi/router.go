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
	"github.com/moddle/moddle/server/internal/commands"
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
	"github.com/moddle/moddle/server/internal/preferences"
	"github.com/moddle/moddle/server/internal/ratelimit"
	"github.com/moddle/moddle/server/internal/reactions"
	"github.com/moddle/moddle/server/internal/reminders"
	"github.com/moddle/moddle/server/internal/savedposts"
	"github.com/moddle/moddle/server/internal/scheduled"
	"github.com/moddle/moddle/server/internal/sidebar"
	"github.com/moddle/moddle/server/internal/slashcmd"
	"github.com/moddle/moddle/server/internal/store"
	"github.com/moddle/moddle/server/internal/teams"
	"github.com/moddle/moddle/server/internal/threads"
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
		cfg:        cfg,
		auth:       authSvc,
		teams:      teamSvc,
		channels:   channelSvc,
		posts:      postSvc,
		reactions:  reactionSvc,
		files:      fileSvc,
		status:     statusSvc,
		audit:      auditSvc,
		slash:      slashSvc,
		bots:       botSvc,
		incoming:   incomingSvc,
		outgoing:   outgoingSvc,
		outDisp:    outDispatcher,
		emojis:     emojiSvc,
		oauthReg:   oauthReg,
		oauthIdent: oauthIdent,
		invites:    inviteSvc,
		saved:      savedSvc,
		links:      linkSvc,
		scheduled:  scheduledSvc,
		reminders:  reminderSvc,
		prefs:      prefsSvc,
		sidebar:    sidebarSvc,
		commands:   commandSvc,
		threads:    threadSvc,
		hub:        hub,
		host:       host,
		logger:     logger,
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

			// Phase 28: interactive dialog compatibility. These are
			// auth-chain routes because slash commands and post actions
			// can open or submit dialogs on behalf of regular users.
			r.Post("/actions/dialogs/open", h.openDialog)
			r.Post("/actions/dialogs/lookup", h.lookupDialog)
			r.Post("/actions/dialogs/submit", h.submitDialog)

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
