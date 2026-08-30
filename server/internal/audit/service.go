// Package audit writes admin-visible action records to the audit_logs table.
// Calls are fire-and-forget — a failure to log must never block the primary
// action, so all errors are swallowed after a debug log. The table is append-
// only from this service's perspective; operators prune via external jobs.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/hkjang/moyro/server/internal/store"
)

// Canonical action names. Kept as constants so scanners/greps find every
// callsite and so misspellings surface at compile time.
const (
	ActionUserLogin         = "user.login"
	ActionUserLoginFailed   = "user.login.failed"
	ActionUserLogout        = "user.logout"
	ActionUserRegister      = "user.register"
	ActionUserPasswordChg   = "user.password.change"
	ActionUserProfileUpdate = "user.profile.update"
	ActionTeamCreate        = "team.create"
	ActionChannelCreate     = "channel.create"
	ActionChannelPatch      = "channel.patch"
	ActionChannelDirectOpen = "channel.direct.open"
	ActionPostCreate        = "post.create"
	ActionPostDelete        = "post.delete"
	ActionPostPin           = "post.pin"
	ActionPostUnpin         = "post.unpin"
	ActionMemberAdd         = "channel.member.add"
	ActionMemberRemove      = "channel.member.remove"
	ActionCommandExecute    = "command.execute"
	ActionEmojiCreate       = "emoji.create"
	ActionEmojiDelete       = "emoji.delete"
	// Phase 16
	ActionInviteCreate   = "team.invite.create"
	ActionInviteRevoke   = "team.invite.revoke"
	ActionInviteConsume  = "team.invite.consume"
	ActionUserDeactivate = "user.deactivate"
	ActionUserReactivate = "user.reactivate"
	ActionSessionRevoke  = "user.session.revoke"
	ActionChannelArchive = "channel.archive"
	ActionChannelRestore = "channel.restore"
	// Phase 23
	ActionUserPatch       = "user.patch"
	ActionUserActiveSet   = "user.active.set"
	ActionUserImageDelete = "user.image.delete"
	ActionChannelPrivacy  = "channel.privacy"
	ActionChannelDelete   = "channel.delete"
	ActionCommandCreate   = "command.create"
	ActionCommandUpdate   = "command.update"
	ActionCommandDelete   = "command.delete"
	ActionCommandRegen    = "command.regen"
	ActionCommandMove     = "command.move"
	// Phase 24
	ActionTeamUpdate         = "team.update"
	ActionTeamPrivacy        = "team.privacy"
	ActionHookIncomingUpdate = "hook.incoming.update"
	ActionHookOutgoingUpdate = "hook.outgoing.update"
	ActionBotUpdate          = "bot.update"
	ActionUserRolesSet       = "user.roles.set"
	ActionUserPasswordReset  = "user.password.reset"
	ActionThreadFollow       = "thread.follow"
	ActionThreadRead         = "thread.read"
	ActionThreadReadAll      = "thread.read.all"
	ActionCustomStatusSet    = "user.custom_status.set"
	ActionCustomStatusClear  = "user.custom_status.clear"
	ActionPostSetUnread      = "post.set_unread"
	// Phase 25
	ActionChannelMemberRoles  = "channel.member.roles"
	ActionChannelMemberNotify = "channel.member.notify_props"
	ActionTeamMemberRoles     = "team.member.roles"
	ActionSessionRevokeAll    = "user.session.revoke_all"
	ActionSessionRevokeGlobal = "user.session.revoke_all_global"
	ActionUserPromote         = "user.promote"
	ActionUserDemote          = "user.demote"
	ActionThreadSetUnread     = "thread.set_unread"
	ActionUserTyping          = "user.typing"
	// Phase 26
	ActionTokenDisable      = "user.token.disable"
	ActionTokenEnable       = "user.token.enable"
	ActionTokenRevoke       = "user.token.revoke"
	ActionTeamRestore       = "team.restore"
	ActionTeamInviteRegen   = "team.invite.regen"
	ActionTeamInviteEmail   = "team.invite.email"
	ActionPostMove          = "post.move"
	ActionPostRestore       = "post.restore"
	ActionUserConvertToBot  = "user.convert_to_bot"
	ActionUserResetAttempts = "user.reset_failed_attempts"
	ActionHookOutgoingRegen = "hook.outgoing.regen"
	// Phase 27
	ActionBotEnable        = "bot.enable"
	ActionBotConvertToUser = "bot.convert_to_user"
	ActionBotAssign        = "bot.assign"
	ActionPostEphemeral    = "post.ephemeral"
	ActionPostAction       = "post.action"
	ActionEmailVerify      = "user.email.verify"
	ActionEmailVerifySend  = "user.email.verify.send"
	ActionPasswordResetReq = "user.password.reset.request"
	ActionPasswordResetDo  = "user.password.reset.do"
	ActionNotificationsAck = "user.notifications.ack"
	// Phase 28
	ActionPostAck            = "post.ack"
	ActionPostUnack          = "post.unack"
	ActionTosUpdate          = "tos.update"
	ActionTosAccept          = "tos.accept"
	ActionMFAEnable          = "user.mfa.enable"
	ActionMFADisable         = "user.mfa.disable"
	ActionMFAGenerate        = "user.mfa.generate"
	ActionChannelMembersBulk = "channel.members.bulk_add"
	ActionUserAuthSet        = "user.auth.set"
	ActionUserSearch         = "user.search"
	// Phase 29
	ActionBookmarkCreate   = "channel.bookmark.create"
	ActionBookmarkUpdate   = "channel.bookmark.update"
	ActionBookmarkDelete   = "channel.bookmark.delete"
	ActionBookmarkReorder  = "channel.bookmark.reorder"
	ActionDiagnosticsTest  = "admin.diagnostics.test"
	ActionCachesInvalidate = "admin.caches.invalidate"
	ActionDatabaseRecycle  = "admin.database.recycle"
	ActionRedirectLookup   = "redirect.lookup"
	// Phase 30
	ActionTeamDelete         = "team.delete"
	ActionTeamMemberRemove   = "team.member.remove"
	ActionTeamImageDelete    = "team.image.delete"
	ActionTeamImageUpload    = "team.image.upload"
	ActionTeamInvitesRevoke  = "team.invites.email.revoke"
	ActionChannelMove        = "channel.move"
	ActionChannelModerations = "channel.moderations.patch"
	ActionChannelSchemeSet   = "channel.scheme.set"
	ActionViewCreate         = "channel.view.create"
	ActionViewDelete         = "channel.view.delete"
	ActionLoginSwitch        = "user.login.switch"
	ActionUserEmailVerifyAdm = "user.email.verify.member"
	// Phase 31
	ActionBotIconUpload      = "bot.icon.upload"
	ActionBotIconDelete      = "bot.icon.delete"
	ActionFileSearch         = "file.search"
	ActionUploadInit         = "upload.init"
	ActionUploadChunk        = "upload.chunk"
	ActionGroupChannelCreate = "channel.group.create"
	ActionUserBulkDelete     = "user.bulk_delete"
	ActionStatusRecentClear  = "user.status.recent.clear"
	ActionThreadFollowDelete = "thread.follow.delete"
	ActionTeamInviteGuests   = "team.invite.guests.email"
	ActionAutotranslation    = "channel.member.autotranslation"
	// Phase 32
	ActionCustomFieldCreate = "custom_profile.field.create"
	ActionCustomFieldPatch  = "custom_profile.field.patch"
	ActionCustomFieldDelete = "custom_profile.field.delete"
	ActionCustomValuesPatch = "custom_profile.values.patch"
	ActionRecapCreate       = "recap.create"
	ActionRecapDelete       = "recap.delete"
	ActionRecapRead         = "recap.read"
	ActionRecapRegenerate   = "recap.regenerate"
	ActionOAuthAppCreate    = "oauth.app.create"
	ActionOAuthAppUpdate    = "oauth.app.update"
	ActionOAuthAppDelete    = "oauth.app.delete"
	ActionOAuthAppRegen     = "oauth.app.regen_secret"
	ActionPostBurn          = "post.burn"
	ActionPostRewrite       = "post.rewrite"
	ActionTeamImport        = "team.import"
	ActionRestart           = "admin.restart"
	// Moyro Flow work objects.
	ActionWorkItemCreate = "work_item.create"
	ActionWorkItemUpdate = "work_item.update"
	ActionWorkItemDelete = "work_item.delete"
)

type Entry struct {
	ID       int64           `json:"id"`
	ActorID  string          `json:"actor_id"`
	Action   string          `json:"action"`
	Target   string          `json:"target"`
	Payload  json.RawMessage `json:"payload"`
	CreateAt int64           `json:"create_at"`
}

type Service struct {
	db     *store.DB
	logger *slog.Logger
}

func New(db *store.DB, logger *slog.Logger) *Service {
	return &Service{db: db, logger: logger}
}

// Log writes a single audit record. Errors are logged at debug and swallowed
// so callers don't need to worry about failure paths in hot handlers.
func (s *Service) Log(ctx context.Context, actorID, action, target string, payload map[string]any) {
	raw := []byte("null")
	if payload != nil {
		if b, err := json.Marshal(payload); err == nil {
			raw = b
		}
	}
	_, err := s.db.Pool.Exec(ctx, `
		INSERT INTO audit_logs (actor_id, action, target, payload, create_at)
		VALUES ($1,$2,$3,$4,$5)
	`, nullIfEmpty(actorID), action, nullIfEmpty(target), raw, time.Now().UnixMilli())
	if err != nil {
		s.logger.Debug("audit write failed", "action", action, "err", err)
	}
}

// LogAsync spawns a goroutine so the caller's request path isn't tied to
// the audit insert latency. Use this from hot post/message handlers.
func (s *Service) LogAsync(actorID, action, target string, payload map[string]any) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		s.Log(ctx, actorID, action, target, payload)
	}()
}

// List returns the newest N entries, optionally filtered by action prefix
// and actor_id. Empty strings mean "no filter". Used by the admin console
// audit log tab.
func (s *Service) List(ctx context.Context, limit int, actionPrefix, actorID string) ([]Entry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Pool.Query(ctx, `
		SELECT id, COALESCE(actor_id,''), action, COALESCE(target,''), COALESCE(payload,'null'::jsonb), create_at
		FROM audit_logs
		WHERE ($2 = '' OR action LIKE $2 || '%')
		  AND ($3 = '' OR actor_id = $3)
		ORDER BY id DESC
		LIMIT $1
	`, limit, actionPrefix, actorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.ActorID, &e.Action, &e.Target, &e.Payload, &e.CreateAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
