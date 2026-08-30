package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	mmmodel "github.com/mattermost/mattermost/server/public/model"
	mmplugin "github.com/mattermost/mattermost/server/public/plugin"

	"github.com/hkjang/moyro/server/internal/channels"
	"github.com/hkjang/moyro/server/internal/posts"
	"github.com/hkjang/moyro/server/internal/store"
)

const pluginAPITimeout = 15 * time.Second

// mattermostAPI intentionally implements the subset exercised by the
// supported Mattermost binary compatibility tier. Embedding the upstream API
// keeps the wire server ABI complete while the concrete methods below prevent
// supported calls from ever reaching the nil embedded implementation.
// Unsupported APIs remain outside Moyro's compatibility contract.
type mattermostAPI struct {
	mmplugin.API
	host       *Host
	db         *store.DB
	pluginID   string
	generation *Plugin
	logger     *slog.Logger
}

func (a *mattermostAPI) withContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), pluginAPITimeout)
}

func (a *mattermostAPI) acquireGeneration() (func(), error) {
	if a == nil || a.host == nil {
		return nil, errors.New("plugin runtime is unavailable")
	}
	lease, ok := a.host.acquirePluginAPICall(a.pluginID, a.generation)
	if !ok {
		return nil, fmt.Errorf("plugin %q generation is no longer active", a.pluginID)
	}
	return lease.release, nil
}

func pluginAppError(where string, err error) *mmmodel.AppError {
	if err == nil {
		return nil
	}
	status := http.StatusInternalServerError
	if err == pgx.ErrNoRows {
		status = http.StatusNotFound
	}
	return mmmodel.NewAppError(where, "moyro.plugin.api.app_error", nil, err.Error(), status)
}

func (a *mattermostAPI) LoadPluginConfiguration(dest any) error {
	release, err := a.acquireGeneration()
	if err != nil {
		return err
	}
	defer release()
	if dest == nil {
		return fmt.Errorf("configuration destination is required")
	}
	config, err := a.host.loadConfiguration(a.pluginID)
	if err != nil {
		return err
	}
	config = coercePluginConfiguration(config, dest)
	raw, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("marshal plugin configuration: %w", err)
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		return fmt.Errorf("decode plugin configuration: %w", err)
	}
	return nil
}

func coercePluginConfiguration(config map[string]any, dest any) map[string]any {
	result := make(map[string]any, len(config))
	for key, value := range config {
		result[key] = value
	}
	typeOf := reflect.TypeOf(dest)
	if typeOf == nil || typeOf.Kind() != reflect.Pointer {
		return result
	}
	typeOf = typeOf.Elem()
	if typeOf.Kind() != reflect.Struct {
		return result
	}
	for index := 0; index < typeOf.NumField(); index++ {
		field := typeOf.Field(index)
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "" {
			name = field.Name
		}
		for key, value := range result {
			if !strings.EqualFold(key, name) {
				continue
			}
			target := field.Type
			if target.Kind() == reflect.Pointer {
				target = target.Elem()
			}
			raw, ok := value.(string)
			if !ok {
				break
			}
			switch target.Kind() {
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				if parsed, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); err == nil {
					result[key] = parsed
				}
			case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
				if parsed, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64); err == nil {
					result[key] = parsed
				}
			case reflect.Bool:
				if parsed, err := strconv.ParseBool(strings.TrimSpace(raw)); err == nil {
					result[key] = parsed
				}
			}
			break
		}
	}
	return result
}

func (a *mattermostAPI) GetPluginConfig() map[string]any {
	release, acquireErr := a.acquireGeneration()
	if acquireErr != nil {
		a.logger.Error("plugin configuration generation rejected", "plugin", a.pluginID, "err", acquireErr)
		return map[string]any{}
	}
	defer release()
	config, err := a.host.loadConfiguration(a.pluginID)
	if err != nil {
		a.logger.Error("plugin configuration read failed", "plugin", a.pluginID, "err", err)
		return map[string]any{}
	}
	return config
}

func (a *mattermostAPI) SavePluginConfig(config map[string]any) *mmmodel.AppError {
	release, err := a.acquireGeneration()
	if err != nil {
		return pluginAppError("SavePluginConfig", err)
	}
	defer release()
	ctx, cancel := a.withContext()
	defer cancel()
	return pluginAppError("SavePluginConfig", a.host.updateConfiguration(ctx, a.pluginID, config, true))
}

func (a *mattermostAPI) GetBundlePath() (string, error) {
	release, err := a.acquireGeneration()
	if err != nil {
		return "", err
	}
	defer release()
	p, ok := a.host.plugin(a.pluginID)
	if !ok || p != a.generation {
		return "", fmt.Errorf("plugin %q is not installed", a.pluginID)
	}
	return p.Dir, nil
}

func (a *mattermostAPI) GetServerVersion() string {
	release, err := a.acquireGeneration()
	if err != nil {
		return ""
	}
	defer release()
	return mmmodel.CurrentVersion
}

func (a *mattermostAPI) GetUser(userID string) (*mmmodel.User, *mmmodel.AppError) {
	return a.getUser("id", userID)
}

func (a *mattermostAPI) GetUserByUsername(username string) (*mmmodel.User, *mmmodel.AppError) {
	return a.getUser("username", strings.ToLower(strings.TrimSpace(username)))
}

func (a *mattermostAPI) getUser(column, value string) (*mmmodel.User, *mmmodel.AppError) {
	release, err := a.acquireGeneration()
	if err != nil {
		return nil, pluginAppError("GetUser", err)
	}
	defer release()
	if a.db == nil || (column != "id" && column != "username") {
		return nil, pluginAppError("GetUser", fmt.Errorf("plugin database is unavailable"))
	}
	ctx, cancel := a.withContext()
	defer cancel()
	var user mmmodel.User
	err = a.db.Pool.QueryRow(ctx, `
		SELECT id, username, email, roles, create_at, update_at, delete_at,
		       COALESCE(first_name,''), COALESCE(last_name,''),
		       COALESCE(nickname,''), COALESCE(position,''),
		       COALESCE(is_bot,FALSE), COALESCE(bot_description,'')
		FROM users WHERE `+column+`=$1 AND delete_at=0
	`, value).Scan(
		&user.Id, &user.Username, &user.Email, &user.Roles,
		&user.CreateAt, &user.UpdateAt, &user.DeleteAt,
		&user.FirstName, &user.LastName, &user.Nickname, &user.Position,
		&user.IsBot, &user.BotDescription,
	)
	if err != nil {
		return nil, pluginAppError("GetUser", err)
	}
	return &user, nil
}

func (a *mattermostAPI) GetChannel(channelID string) (*mmmodel.Channel, *mmmodel.AppError) {
	release, err := a.acquireGeneration()
	if err != nil {
		return nil, pluginAppError("GetChannel", err)
	}
	defer release()
	if a.db == nil {
		return nil, pluginAppError("GetChannel", fmt.Errorf("plugin database is unavailable"))
	}
	ctx, cancel := a.withContext()
	defer cancel()
	c, err := channels.New(a.db).Get(ctx, channelID)
	if err != nil {
		return nil, pluginAppError("GetChannel", err)
	}
	return toMattermostChannel(c), nil
}

func (a *mattermostAPI) GetChannelMember(channelID, userID string) (*mmmodel.ChannelMember, *mmmodel.AppError) {
	release, err := a.acquireGeneration()
	if err != nil {
		return nil, pluginAppError("GetChannelMember", err)
	}
	defer release()
	if a.db == nil {
		return nil, pluginAppError("GetChannelMember", fmt.Errorf("plugin database is unavailable"))
	}
	ctx, cancel := a.withContext()
	defer cancel()
	m, err := channels.New(a.db).GetMember(ctx, channelID, userID)
	if err != nil {
		return nil, pluginAppError("GetChannelMember", err)
	}
	if m == nil {
		return nil, pluginAppError("GetChannelMember", pgx.ErrNoRows)
	}
	return &mmmodel.ChannelMember{
		ChannelId:    m.ChannelID,
		UserId:       m.UserID,
		Roles:        m.Roles,
		LastViewedAt: m.LastViewedAt,
	}, nil
}

func (a *mattermostAPI) GetPostsForChannel(channelID string, page, perPage int) (*mmmodel.PostList, *mmmodel.AppError) {
	release, err := a.acquireGeneration()
	if err != nil {
		return nil, pluginAppError("GetPostsForChannel", err)
	}
	defer release()
	if a.db == nil {
		return nil, pluginAppError("GetPostsForChannel", fmt.Errorf("plugin database is unavailable"))
	}
	ctx, cancel := a.withContext()
	defer cancel()
	list, err := posts.New(a.db).ListForChannel(ctx, channelID, page, perPage)
	if err != nil {
		return nil, pluginAppError("GetPostsForChannel", err)
	}
	return toMattermostPostList(list), nil
}

func toMattermostChannel(c *channels.Channel) *mmmodel.Channel {
	if c == nil {
		return nil
	}
	return &mmmodel.Channel{
		Id: c.ID, TeamId: c.TeamID, Type: mmmodel.ChannelType(c.Type),
		DisplayName: c.DisplayName, Name: c.Name, Header: c.Header,
		Purpose: c.Purpose, CreateAt: c.CreateAt, UpdateAt: c.UpdateAt,
		DeleteAt: c.DeleteAt, Props: map[string]any{},
	}
}

func toMattermostPost(p *posts.Post) *mmmodel.Post {
	if p == nil {
		return nil
	}
	out := &mmmodel.Post{
		Id: p.ID, ChannelId: p.ChannelID, UserId: p.UserID, RootId: p.RootID,
		Message: p.Message, Type: p.Type, FileIds: append([]string(nil), p.FileIDs...),
		IsPinned: p.IsPinned, CreateAt: p.CreateAt, UpdateAt: p.UpdateAt,
		DeleteAt: p.DeleteAt,
	}
	out.SetProps(mmmodel.StringInterface(p.Props))
	return out
}

func toMattermostPostList(list *posts.PostList) *mmmodel.PostList {
	out := mmmodel.NewPostList()
	if list == nil {
		return out
	}
	out.Order = append(out.Order, list.Order...)
	for id, post := range list.Posts {
		out.Posts[id] = toMattermostPost(post)
	}
	return out
}

func (a *mattermostAPI) HasPermissionTo(userID string, permission *mmmodel.Permission) bool {
	release, err := a.acquireGeneration()
	if err != nil {
		return false
	}
	defer release()
	if a.db == nil || permission == nil {
		return false
	}
	ctx, cancel := a.withContext()
	defer cancel()
	var roles string
	if err := a.db.Pool.QueryRow(ctx, `SELECT roles FROM users WHERE id=$1 AND delete_at=0`, userID).Scan(&roles); err != nil {
		return false
	}
	for _, role := range strings.Fields(roles) {
		if role == "system_admin" {
			return true
		}
	}
	return false
}

func (a *mattermostAPI) LogDebug(msg string, fields ...any) { a.log(slog.LevelDebug, msg, fields...) }
func (a *mattermostAPI) LogInfo(msg string, fields ...any)  { a.log(slog.LevelInfo, msg, fields...) }
func (a *mattermostAPI) LogWarn(msg string, fields ...any)  { a.log(slog.LevelWarn, msg, fields...) }
func (a *mattermostAPI) LogError(msg string, fields ...any) { a.log(slog.LevelError, msg, fields...) }

func (a *mattermostAPI) log(level slog.Level, msg string, fields ...any) {
	release, err := a.acquireGeneration()
	if err != nil {
		return
	}
	defer release()
	attrs := []any{"plugin", a.pluginID}
	for i := 0; i+1 < len(fields); i += 2 {
		key, ok := fields[i].(string)
		if !ok || key == "" {
			continue
		}
		attrs = append(attrs, key, fields[i+1])
	}
	a.logger.Log(context.Background(), level, msg, attrs...)
}

func (a *mattermostAPI) KVSet(key string, value []byte) *mmmodel.AppError {
	_, appErr := a.KVSetWithOptions(key, value, mmmodel.PluginKVSetOptions{})
	return appErr
}

func (a *mattermostAPI) KVSetWithExpiry(key string, value []byte, expireInSeconds int64) *mmmodel.AppError {
	_, appErr := a.KVSetWithOptions(key, value, mmmodel.PluginKVSetOptions{ExpireInSeconds: expireInSeconds})
	return appErr
}

func (a *mattermostAPI) KVCompareAndSet(key string, oldValue, newValue []byte) (bool, *mmmodel.AppError) {
	return a.KVSetWithOptions(key, newValue, mmmodel.PluginKVSetOptions{Atomic: true, OldValue: oldValue})
}

func (a *mattermostAPI) KVSetWithOptions(key string, value []byte, options mmmodel.PluginKVSetOptions) (bool, *mmmodel.AppError) {
	release, err := a.acquireGeneration()
	if err != nil {
		return false, pluginAppError("KVSetWithOptions", err)
	}
	defer release()
	if err := validatePluginKey(key); err != nil {
		return false, pluginAppError("KVSetWithOptions", err)
	}
	if a.db == nil {
		return false, pluginAppError("KVSetWithOptions", fmt.Errorf("plugin database is unavailable"))
	}
	ctx, cancel := a.withContext()
	defer cancel()
	now := time.Now().UnixMilli()
	expireAt := int64(0)
	if options.ExpireInSeconds > 0 {
		expireAt = now + options.ExpireInSeconds*1000
	}
	if value == nil {
		if options.Atomic && options.OldValue != nil {
			tag, err := a.db.Pool.Exec(ctx, `
				DELETE FROM plugin_key_values
				WHERE plugin_id=$1 AND key=$2 AND value=$3 AND (expire_at=0 OR expire_at>$4)
			`, a.pluginID, key, options.OldValue, now)
			return err == nil && tag.RowsAffected() == 1, pluginAppError("KVSetWithOptions", err)
		}
		if options.Atomic {
			var exists bool
			err := a.db.Pool.QueryRow(ctx, `
				SELECT EXISTS (SELECT 1 FROM plugin_key_values
				WHERE plugin_id=$1 AND key=$2 AND (expire_at=0 OR expire_at>$3))
			`, a.pluginID, key, now).Scan(&exists)
			return err == nil && !exists, pluginAppError("KVSetWithOptions", err)
		}
		_, err := a.db.Pool.Exec(ctx, `DELETE FROM plugin_key_values WHERE plugin_id=$1 AND key=$2`, a.pluginID, key)
		return err == nil, pluginAppError("KVSetWithOptions", err)
	}
	if !options.Atomic {
		_, err := a.db.Pool.Exec(ctx, `
			INSERT INTO plugin_key_values (plugin_id,key,value,expire_at,create_at,update_at)
			VALUES ($1,$2,$3,$4,$5,$5)
			ON CONFLICT (plugin_id,key) DO UPDATE
			SET value=EXCLUDED.value, expire_at=EXCLUDED.expire_at, update_at=EXCLUDED.update_at
		`, a.pluginID, key, value, expireAt, now)
		return err == nil, pluginAppError("KVSetWithOptions", err)
	}

	// Expired rows are absent for CAS purposes. Deleting first also permits
	// an OldValue=nil insertion to claim a lapsed cluster lease.
	if _, err := a.db.Pool.Exec(ctx, `
		DELETE FROM plugin_key_values
		WHERE plugin_id=$1 AND key=$2 AND expire_at > 0 AND expire_at <= $3
	`, a.pluginID, key, now); err != nil {
		return false, pluginAppError("KVSetWithOptions", err)
	}
	if options.OldValue == nil {
		tag, err := a.db.Pool.Exec(ctx, `
			INSERT INTO plugin_key_values (plugin_id,key,value,expire_at,create_at,update_at)
			VALUES ($1,$2,$3,$4,$5,$5) ON CONFLICT (plugin_id,key) DO NOTHING
		`, a.pluginID, key, value, expireAt, now)
		return err == nil && tag.RowsAffected() == 1, pluginAppError("KVSetWithOptions", err)
	}
	tag, err := a.db.Pool.Exec(ctx, `
		UPDATE plugin_key_values SET value=$3, expire_at=$4, update_at=$5
		WHERE plugin_id=$1 AND key=$2 AND value=$6
	`, a.pluginID, key, value, expireAt, now, options.OldValue)
	return err == nil && tag.RowsAffected() == 1, pluginAppError("KVSetWithOptions", err)
}

func (a *mattermostAPI) KVGet(key string) ([]byte, *mmmodel.AppError) {
	release, err := a.acquireGeneration()
	if err != nil {
		return nil, pluginAppError("KVGet", err)
	}
	defer release()
	if err := validatePluginKey(key); err != nil {
		return nil, pluginAppError("KVGet", err)
	}
	if a.db == nil {
		return nil, pluginAppError("KVGet", fmt.Errorf("plugin database is unavailable"))
	}
	ctx, cancel := a.withContext()
	defer cancel()
	var value []byte
	err = a.db.Pool.QueryRow(ctx, `
		SELECT value FROM plugin_key_values
		WHERE plugin_id=$1 AND key=$2 AND (expire_at=0 OR expire_at>$3)
	`, a.pluginID, key, time.Now().UnixMilli()).Scan(&value)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return value, pluginAppError("KVGet", err)
}

func (a *mattermostAPI) KVDelete(key string) *mmmodel.AppError {
	release, err := a.acquireGeneration()
	if err != nil {
		return pluginAppError("KVDelete", err)
	}
	defer release()
	if err := validatePluginKey(key); err != nil {
		return pluginAppError("KVDelete", err)
	}
	if a.db == nil {
		return pluginAppError("KVDelete", fmt.Errorf("plugin database is unavailable"))
	}
	ctx, cancel := a.withContext()
	defer cancel()
	_, err = a.db.Pool.Exec(ctx, `DELETE FROM plugin_key_values WHERE plugin_id=$1 AND key=$2`, a.pluginID, key)
	return pluginAppError("KVDelete", err)
}

func (a *mattermostAPI) KVCompareAndDelete(key string, oldValue []byte) (bool, *mmmodel.AppError) {
	release, err := a.acquireGeneration()
	if err != nil {
		return false, pluginAppError("KVCompareAndDelete", err)
	}
	defer release()
	if err := validatePluginKey(key); err != nil {
		return false, pluginAppError("KVCompareAndDelete", err)
	}
	if a.db == nil {
		return false, pluginAppError("KVCompareAndDelete", fmt.Errorf("plugin database is unavailable"))
	}
	ctx, cancel := a.withContext()
	defer cancel()
	tag, err := a.db.Pool.Exec(ctx, `
		DELETE FROM plugin_key_values
		WHERE plugin_id=$1 AND key=$2 AND value=$3 AND (expire_at=0 OR expire_at>$4)
	`, a.pluginID, key, oldValue, time.Now().UnixMilli())
	return err == nil && tag.RowsAffected() == 1, pluginAppError("KVCompareAndDelete", err)
}

func (a *mattermostAPI) KVDeleteAll() *mmmodel.AppError {
	release, err := a.acquireGeneration()
	if err != nil {
		return pluginAppError("KVDeleteAll", err)
	}
	defer release()
	if a.db == nil {
		return pluginAppError("KVDeleteAll", fmt.Errorf("plugin database is unavailable"))
	}
	ctx, cancel := a.withContext()
	defer cancel()
	_, err = a.db.Pool.Exec(ctx, `DELETE FROM plugin_key_values WHERE plugin_id=$1`, a.pluginID)
	return pluginAppError("KVDeleteAll", err)
}

func (a *mattermostAPI) KVList(page, perPage int) ([]string, *mmmodel.AppError) {
	release, err := a.acquireGeneration()
	if err != nil {
		return nil, pluginAppError("KVList", err)
	}
	defer release()
	if a.db == nil {
		return nil, pluginAppError("KVList", fmt.Errorf("plugin database is unavailable"))
	}
	if page < 0 {
		page = 0
	}
	if perPage <= 0 || perPage > 200 {
		perPage = 60
	}
	ctx, cancel := a.withContext()
	defer cancel()
	rows, err := a.db.Pool.Query(ctx, `
		SELECT key FROM plugin_key_values
		WHERE plugin_id=$1 AND (expire_at=0 OR expire_at>$2)
		ORDER BY key LIMIT $3 OFFSET $4
	`, a.pluginID, time.Now().UnixMilli(), perPage, page*perPage)
	if err != nil {
		return nil, pluginAppError("KVList", err)
	}
	defer rows.Close()
	keys := []string{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, pluginAppError("KVList", err)
		}
		keys = append(keys, key)
	}
	return keys, pluginAppError("KVList", rows.Err())
}

func validatePluginKey(key string) error {
	if key == "" || !utf8.ValidString(key) || utf8.RuneCountInString(key) > mmmodel.KeyValueKeyMaxRunes {
		return fmt.Errorf("plugin key must contain 1-%d valid UTF-8 characters", mmmodel.KeyValueKeyMaxRunes)
	}
	return nil
}

// equalPluginValue is kept small and explicit for CAS unit tests.
func equalPluginValue(left, right []byte) bool { return bytes.Equal(left, right) }
