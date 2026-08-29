package ws

import (
	"context"

	"github.com/hkjang/moyro/server/internal/store"
)

// DatabaseAudienceResolver resolves scoped WebSocket events to active
// members. Channel scope takes precedence when both fields are present because
// it is the narrower authorization boundary.
func DatabaseAudienceResolver(db *store.DB) AudienceResolver {
	return func(ctx context.Context, b Broadcast) (map[string]struct{}, error) {
		query := ""
		arg := ""
		switch {
		case b.ChannelID != "":
			query = `
				SELECT cm.user_id
				FROM channel_members AS cm
				JOIN users AS u ON u.id = cm.user_id
				WHERE cm.channel_id = $1 AND u.delete_at = 0
			`
			arg = b.ChannelID
		case b.TeamID != "":
			query = `
				SELECT tm.user_id
				FROM team_members AS tm
				JOIN users AS u ON u.id = tm.user_id
				WHERE tm.team_id = $1 AND u.delete_at = 0
			`
			arg = b.TeamID
		default:
			return nil, nil
		}

		rows, err := db.Pool.Query(ctx, query, arg)
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
