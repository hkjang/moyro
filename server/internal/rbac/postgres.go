package rbac

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresRepository struct{ pool *pgxpool.Pool }

func NewPostgres(pool *pgxpool.Pool) (*Service, error) {
	if pool == nil {
		return nil, errors.New("rbac: nil postgres pool")
	}
	return New(&PostgresRepository{pool: pool})
}

func (r *PostgresRepository) EffectivePermissions(ctx context.Context, userID string, scope Scope) (map[string]struct{}, error) {
	rows, err := r.pool.Query(ctx, `
		WITH assigned(role_name) AS (
			SELECT unnest(regexp_split_to_array(BTRIM(u.roles), E'\\s+'))
			  FROM users u WHERE u.id=$1 AND u.delete_at=0
			UNION
			SELECT unnest(regexp_split_to_array(BTRIM(tm.roles), E'\\s+'))
			  FROM team_members tm WHERE $2<>'' AND tm.user_id=$1 AND tm.team_id=$2
			UNION
			SELECT unnest(regexp_split_to_array(BTRIM(cm.roles), E'\\s+'))
			  FROM channel_members cm WHERE $3<>'' AND cm.user_id=$1 AND cm.channel_id=$3
		)
		SELECT DISTINCT rp.permission_name
		  FROM assigned a
		  JOIN roles r ON r.name=a.role_name
		  JOIN role_permissions rp ON rp.role_id=r.id
		 ORDER BY rp.permission_name
	`, userID, scope.TeamID, scope.ChannelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = struct{}{}
	}
	return out, rows.Err()
}

func (r *PostgresRepository) ListPermissions(ctx context.Context) ([]Permission, error) {
	rows, err := r.pool.Query(ctx, `SELECT name, description, resource_type, built_in FROM permissions ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Permission{}
	for rows.Next() {
		var permission Permission
		if err := rows.Scan(&permission.Name, &permission.Description, &permission.ResourceType, &permission.BuiltIn); err != nil {
			return nil, err
		}
		out = append(out, permission)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) GetRole(ctx context.Context, roleID string) (Role, error) {
	role, err := scanRole(r.pool.QueryRow(ctx, roleQuery+` WHERE r.id=$1 OR r.name=$1 GROUP BY r.id`, roleID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Role{}, ErrNotFound
	}
	return role, err
}

func (r *PostgresRepository) ListRoles(ctx context.Context) ([]Role, error) {
	rows, err := r.pool.Query(ctx, roleQuery+` GROUP BY r.id ORDER BY r.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Role{}
	for rows.Next() {
		role, err := scanRole(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, role)
	}
	return out, rows.Err()
}

const roleQuery = `
	SELECT r.id, r.name, r.display_name, r.description, r.scope_type,
	       r.built_in, r.revision, r.create_at, r.update_at,
	       COALESCE(array_agg(rp.permission_name ORDER BY rp.permission_name)
	           FILTER (WHERE rp.permission_name IS NOT NULL), '{}')
	  FROM roles r LEFT JOIN role_permissions rp ON rp.role_id=r.id`

type scanner interface{ Scan(...any) error }

func scanRole(row scanner) (Role, error) {
	var role Role
	err := row.Scan(&role.ID, &role.Name, &role.DisplayName, &role.Description,
		&role.ScopeType, &role.BuiltIn, &role.Revision, &role.CreateAt, &role.UpdateAt,
		&role.Permissions)
	return role, err
}

func (r *PostgresRepository) ReplaceRolePermissions(ctx context.Context, roleID string, permissions []string, actorID string, expectedRevision *int64, updateAt int64) (Role, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Role{}, err
	}
	defer tx.Rollback(ctx)
	var revision int64
	if err := tx.QueryRow(ctx, `SELECT revision FROM roles WHERE id=$1 FOR UPDATE`, roleID).Scan(&revision); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Role{}, ErrNotFound
		}
		return Role{}, err
	}
	if expectedRevision != nil && *expectedRevision != revision {
		return Role{}, ErrRevisionConflict
	}
	if _, err := tx.Exec(ctx, `DELETE FROM role_permissions WHERE role_id=$1`, roleID); err != nil {
		return Role{}, err
	}
	if len(permissions) != 0 {
		if _, err := tx.Exec(ctx, `
			INSERT INTO role_permissions (role_id, permission_name)
			SELECT $1, unnest($2::text[])
		`, roleID, permissions); err != nil {
			return Role{}, err
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE roles SET revision=revision+1, update_at=$2, updated_by=NULLIF($3,'') WHERE id=$1
	`, roleID, updateAt, actorID); err != nil {
		return Role{}, err
	}
	role, err := scanRole(tx.QueryRow(ctx, roleQuery+` WHERE r.id=$1 GROUP BY r.id`, roleID))
	if err != nil {
		return Role{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Role{}, err
	}
	return role, nil
}
