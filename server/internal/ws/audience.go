package ws

import (
	"context"
	"time"

	"github.com/hkjang/moyro/server/internal/store"
)

// DatabaseAudienceResolver resolves scoped WebSocket events to active users.
// Channel, team, and subject-user scopes are intersected. A subject-user scope
// is used for presence/profile events: active regular users may see it, while
// an active guest may only see itself or a subject with whom it shares a live
// channel. Missing, deleted, and expired subjects fail closed.
//
// The query is driven by the caller's candidate set rather than by the user
// directory. Fan-out only ever delivers to users this instance currently holds
// a socket for, so evaluating the membership predicates against the whole
// `users` table did work proportional to the size of the installation on every
// broadcast. Joining the candidate ids against the primary key keeps the cost
// proportional to the number of connected users instead.
//
// Guest status reads the stored `users.is_guest` projection. The previous
// inline regular expression over the whitespace-separated role string could not
// use an index and ran once per candidate row; migration 000016 proves the
// projection is equivalent to that predicate.
func DatabaseAudienceResolver(db *store.DB) AudienceResolver {
	return func(ctx context.Context, b Broadcast, candidates []string) (map[string]struct{}, error) {
		if b.ChannelID == "" && b.TeamID == "" && b.SubjectUserID == "" {
			return nil, nil
		}
		if len(candidates) == 0 {
			return map[string]struct{}{}, nil
		}

		rows, err := db.Pool.Query(ctx, `
			WITH candidate AS (
				SELECT DISTINCT unnest($4::TEXT[]) AS id
			)
			SELECT recipient.id
			FROM candidate
			JOIN users AS recipient ON recipient.id = candidate.id
			WHERE recipient.delete_at = 0
			  AND (NOT recipient.is_guest OR recipient.guest_expires_at > $5)
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
					  AND (NOT subject.is_guest OR subject.guest_expires_at > $5)
					  AND (
						recipient.id = subject.id
						OR NOT recipient.is_guest
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
		`, b.ChannelID, b.TeamID, b.SubjectUserID, candidates, time.Now().UnixMilli())
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		out := make(map[string]struct{}, len(candidates))
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
