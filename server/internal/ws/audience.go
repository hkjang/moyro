package ws

import (
	"context"

	"github.com/hkjang/moyro/server/internal/store"
)

// DatabaseAudienceResolver resolves scoped WebSocket events to active users.
// Channel, team, and subject-user scopes are intersected. A subject-user scope
// is used for presence/profile events: active regular users may see it, while
// an active guest may only see itself or a subject with whom it shares a live
// channel. Missing, deleted, and expired subjects fail closed.
func DatabaseAudienceResolver(db *store.DB) AudienceResolver {
	return func(ctx context.Context, b Broadcast) (map[string]struct{}, error) {
		if b.ChannelID == "" && b.TeamID == "" && b.SubjectUserID == "" {
			return nil, nil
		}

		rows, err := db.Pool.Query(ctx, `
			SELECT recipient.id
			FROM users AS recipient
			WHERE recipient.delete_at = 0
			  AND (
				recipient.roles !~ '(^|[[:space:]])system_guest([[:space:]]|$)'
				OR recipient.guest_expires_at > (EXTRACT(EPOCH FROM clock_timestamp()) * 1000)::BIGINT
			  )
			  AND (
				$1 = '' OR EXISTS (
					SELECT 1
					FROM channel_members AS scoped_cm
					JOIN channels AS scoped_c ON scoped_c.id = scoped_cm.channel_id AND scoped_c.delete_at = 0
					WHERE scoped_cm.channel_id = $1
					  AND scoped_cm.user_id = recipient.id
					  AND (
						COALESCE(scoped_c.team_id, '') = '' OR EXISTS (
							SELECT 1
							FROM team_members AS scoped_channel_tm
							JOIN teams AS scoped_channel_t
							  ON scoped_channel_t.id = scoped_channel_tm.team_id AND scoped_channel_t.delete_at = 0
							WHERE scoped_channel_tm.team_id = scoped_c.team_id
							  AND scoped_channel_tm.user_id = recipient.id
						)
					  )
				)
			  )
			  AND (
				$2 = '' OR EXISTS (
					SELECT 1
					FROM team_members AS scoped_tm
					JOIN teams AS scoped_t ON scoped_t.id = scoped_tm.team_id AND scoped_t.delete_at = 0
					WHERE scoped_tm.team_id = $2 AND scoped_tm.user_id = recipient.id
				)
			  )
			  AND (
				$1 = '' OR $2 = '' OR EXISTS (
					SELECT 1 FROM channels AS intersected_c
					WHERE intersected_c.id = $1 AND intersected_c.team_id = $2 AND intersected_c.delete_at = 0
				)
			  )
			  AND (
				$3 = '' OR EXISTS (
					SELECT 1
					FROM users AS subject
					WHERE subject.id = $3
					  AND subject.delete_at = 0
					  AND (
						subject.roles !~ '(^|[[:space:]])system_guest([[:space:]]|$)'
						OR subject.guest_expires_at > (EXTRACT(EPOCH FROM clock_timestamp()) * 1000)::BIGINT
					  )
					  AND (
						recipient.id = subject.id
						OR recipient.roles !~ '(^|[[:space:]])system_guest([[:space:]]|$)'
						OR EXISTS (
							SELECT 1
							FROM channel_members AS subject_cm
							JOIN channel_members AS recipient_cm
							  ON recipient_cm.channel_id = subject_cm.channel_id
							 AND recipient_cm.user_id = recipient.id
							JOIN channels AS shared_c
							  ON shared_c.id = subject_cm.channel_id AND shared_c.delete_at = 0
							WHERE subject_cm.user_id = subject.id
							  AND (
								COALESCE(shared_c.team_id, '') = '' OR (
									EXISTS (
										SELECT 1 FROM teams AS shared_t
										WHERE shared_t.id = shared_c.team_id AND shared_t.delete_at = 0
									)
									AND EXISTS (
										SELECT 1 FROM team_members AS subject_tm
										WHERE subject_tm.team_id = shared_c.team_id AND subject_tm.user_id = subject.id
									)
									AND EXISTS (
										SELECT 1 FROM team_members AS recipient_tm
										WHERE recipient_tm.team_id = shared_c.team_id AND recipient_tm.user_id = recipient.id
									)
								)
							  )
						)
					  )
				)
			  )
		`, b.ChannelID, b.TeamID, b.SubjectUserID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		out := make(map[string]struct{})
		for rows.Next() {
			var userID string
			if err := rows.Scan(&userID); err != nil {
				return nil, err
			}
			out[userID] = struct{}{}
		}
		return out, rows.Err()
	}
}
