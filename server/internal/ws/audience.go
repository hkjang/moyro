package ws

import (
	"context"

	"github.com/hkjang/moyro/server/internal/store"
)

// DatabaseAudienceResolver resolves scoped WebSocket events to active members.
// When channel and team scopes are both present, both must match: the channel
// must belong to the team and recipients must retain both memberships.
func DatabaseAudienceResolver(db *store.DB) AudienceResolver {
	return func(ctx context.Context, b Broadcast) (map[string]struct{}, error) {
		query := ""
		args := []any{}
		switch {
		case b.ChannelID != "":
			query = `
				SELECT cm.user_id
				FROM channel_members AS cm
				JOIN channels AS c ON c.id = cm.channel_id AND c.delete_at = 0
				JOIN users AS u ON u.id = cm.user_id
				WHERE cm.channel_id = $1
				  AND u.delete_at = 0
				  AND (
					COALESCE(c.team_id, '') = '' OR EXISTS (
						SELECT 1
						FROM team_members AS channel_tm
						JOIN teams AS channel_t ON channel_t.id = channel_tm.team_id AND channel_t.delete_at = 0
						WHERE channel_tm.team_id = c.team_id AND channel_tm.user_id = cm.user_id
					)
				  )
				  AND (
					$2 = '' OR (
						c.team_id = $2 AND EXISTS (
							SELECT 1
							FROM team_members AS scoped_tm
							JOIN teams AS scoped_t ON scoped_t.id = scoped_tm.team_id AND scoped_t.delete_at = 0
							WHERE scoped_tm.team_id = $2 AND scoped_tm.user_id = cm.user_id
						)
					)
				  )
			`
			args = append(args, b.ChannelID, b.TeamID)
		case b.TeamID != "":
			query = `
				SELECT tm.user_id
				FROM team_members AS tm
				JOIN teams AS t ON t.id = tm.team_id AND t.delete_at = 0
				JOIN users AS u ON u.id = tm.user_id
				WHERE tm.team_id = $1 AND u.delete_at = 0
			`
			args = append(args, b.TeamID)
		default:
			return nil, nil
		}

		rows, err := db.Pool.Query(ctx, query, args...)
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
