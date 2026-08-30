package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	mmmodel "github.com/mattermost/mattermost/server/public/model"

	"github.com/hkjang/moyro/server/internal/application/postcommand"
	"github.com/hkjang/moyro/server/internal/audit"
	"github.com/hkjang/moyro/server/internal/channels"
	"github.com/hkjang/moyro/server/internal/posts"
	"github.com/hkjang/moyro/server/internal/preferences"
	"github.com/hkjang/moyro/server/internal/ws"
)

// RegisterCommand records command ownership on the concrete plugin
// generation. ExecuteCommand consults this set before invoking a hook, so a
// command registered by an old generation cannot leak into its replacement.
func (a *mattermostAPI) RegisterCommand(command *mmmodel.Command) error {
	release, err := a.acquireGeneration()
	if err != nil {
		return err
	}
	defer release()
	if command == nil {
		return errors.New("command is required")
	}
	trigger := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(command.Trigger, "/")))
	if trigger == "" || strings.ContainsAny(trigger, " \t\r\n") {
		return errors.New("command trigger is invalid")
	}
	command.PluginId = a.pluginID
	a.generation.registerCommand(trigger)
	return nil
}

func (a *mattermostAPI) GetUsers(options *mmmodel.UserGetOptions) ([]*mmmodel.User, *mmmodel.AppError) {
	release, err := a.acquireGeneration()
	if err != nil {
		return nil, pluginAppError("GetUsers", err)
	}
	defer release()
	if a.db == nil {
		return nil, pluginAppError("GetUsers", errors.New("plugin database is unavailable"))
	}
	if options == nil {
		options = &mmmodel.UserGetOptions{}
	}
	perPage := options.PerPage
	if perPage <= 0 || perPage > 200 {
		perPage = 60
	}
	page := options.Page
	if page < 0 {
		page = 0
	}

	joins := []string{}
	where := []string{"1=1"}
	args := []any{}
	addArg := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	if options.Active && !options.Inactive {
		where = append(where, "u.delete_at=0")
	} else if options.Inactive && !options.Active {
		where = append(where, "u.delete_at<>0")
	} else if !options.Inactive {
		where = append(where, "u.delete_at=0")
	}
	if options.InTeamId != "" {
		joins = append(joins, "JOIN team_members tm ON tm.user_id=u.id")
		where = append(where, "tm.team_id="+addArg(options.InTeamId))
	}
	if options.InChannelId != "" {
		joins = append(joins, "JOIN channel_members cm ON cm.user_id=u.id")
		where = append(where, "cm.channel_id="+addArg(options.InChannelId))
	}
	if options.UpdatedAfter > 0 {
		where = append(where, "u.update_at>"+addArg(options.UpdatedAfter))
	}
	args = append(args, perPage, page*perPage)
	query := `
		SELECT DISTINCT u.id,u.username,u.email,u.roles,u.create_at,u.update_at,u.delete_at,
		       COALESCE(u.first_name,''),COALESCE(u.last_name,''),COALESCE(u.nickname,''),
		       COALESCE(u.position,''),COALESCE(u.is_bot,FALSE),COALESCE(u.bot_description,'')
		FROM users u ` + strings.Join(joins, " ") + `
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY u.username
		LIMIT $` + fmt.Sprint(len(args)-1) + ` OFFSET $` + fmt.Sprint(len(args))
	ctx, cancel := a.withContext()
	defer cancel()
	rows, queryErr := a.db.Pool.Query(ctx, query, args...)
	if queryErr != nil {
		return nil, pluginAppError("GetUsers", queryErr)
	}
	defer rows.Close()
	result := []*mmmodel.User{}
	for rows.Next() {
		user, scanErr := scanMattermostUser(rows)
		if scanErr != nil {
			return nil, pluginAppError("GetUsers", scanErr)
		}
		result = append(result, user)
	}
	return result, pluginAppError("GetUsers", rows.Err())
}

func (a *mattermostAPI) GetUsersByUsernames(usernames []string) ([]*mmmodel.User, *mmmodel.AppError) {
	release, err := a.acquireGeneration()
	if err != nil {
		return nil, pluginAppError("GetUsersByUsernames", err)
	}
	defer release()
	if a.db == nil || len(usernames) == 0 {
		return []*mmmodel.User{}, nil
	}
	normalized := make([]string, 0, len(usernames))
	for _, username := range usernames {
		if value := strings.ToLower(strings.TrimSpace(username)); value != "" {
			normalized = append(normalized, value)
		}
	}
	ctx, cancel := a.withContext()
	defer cancel()
	rows, queryErr := a.db.Pool.Query(ctx, `
		SELECT id,username,email,roles,create_at,update_at,delete_at,
		       COALESCE(first_name,''),COALESCE(last_name,''),COALESCE(nickname,''),
		       COALESCE(position,''),COALESCE(is_bot,FALSE),COALESCE(bot_description,'')
		FROM users WHERE LOWER(username)=ANY($1) AND delete_at=0
	`, normalized)
	if queryErr != nil {
		return nil, pluginAppError("GetUsersByUsernames", queryErr)
	}
	defer rows.Close()
	byName := map[string]*mmmodel.User{}
	for rows.Next() {
		user, scanErr := scanMattermostUser(rows)
		if scanErr != nil {
			return nil, pluginAppError("GetUsersByUsernames", scanErr)
		}
		byName[strings.ToLower(user.Username)] = user
	}
	if rows.Err() != nil {
		return nil, pluginAppError("GetUsersByUsernames", rows.Err())
	}
	result := make([]*mmmodel.User, 0, len(normalized))
	for _, username := range normalized {
		if user := byName[username]; user != nil {
			result = append(result, user)
		}
	}
	return result, nil
}

type userScanner interface{ Scan(...any) error }

func scanMattermostUser(row userScanner) (*mmmodel.User, error) {
	var user mmmodel.User
	err := row.Scan(
		&user.Id, &user.Username, &user.Email, &user.Roles,
		&user.CreateAt, &user.UpdateAt, &user.DeleteAt,
		&user.FirstName, &user.LastName, &user.Nickname, &user.Position,
		&user.IsBot, &user.BotDescription,
	)
	return &user, err
}

func (a *mattermostAPI) GetPreferenceForUser(userID, category, name string) (mmmodel.Preference, *mmmodel.AppError) {
	release, err := a.acquireGeneration()
	if err != nil {
		return mmmodel.Preference{}, pluginAppError("GetPreferenceForUser", err)
	}
	defer release()
	ctx, cancel := a.withContext()
	defer cancel()
	pref, getErr := preferences.New(a.db).GetByName(ctx, userID, category, name)
	if getErr != nil {
		return mmmodel.Preference{}, pluginAppError("GetPreferenceForUser", getErr)
	}
	return mmmodel.Preference{UserId: pref.UserID, Category: pref.Category, Name: pref.Name, Value: pref.Value}, nil
}

func (a *mattermostAPI) UpdatePreferencesForUser(userID string, values []mmmodel.Preference) *mmmodel.AppError {
	release, err := a.acquireGeneration()
	if err != nil {
		return pluginAppError("UpdatePreferencesForUser", err)
	}
	defer release()
	prefs := make([]preferences.Preference, 0, len(values))
	for _, value := range values {
		prefs = append(prefs, preferences.Preference{UserID: userID, Category: value.Category, Name: value.Name, Value: value.Value})
	}
	ctx, cancel := a.withContext()
	defer cancel()
	return pluginAppError("UpdatePreferencesForUser", preferences.New(a.db).Upsert(ctx, userID, prefs))
}

func (a *mattermostAPI) DeletePreferencesForUser(userID string, values []mmmodel.Preference) *mmmodel.AppError {
	release, err := a.acquireGeneration()
	if err != nil {
		return pluginAppError("DeletePreferencesForUser", err)
	}
	defer release()
	prefs := make([]preferences.Preference, 0, len(values))
	for _, value := range values {
		prefs = append(prefs, preferences.Preference{UserID: userID, Category: value.Category, Name: value.Name})
	}
	ctx, cancel := a.withContext()
	defer cancel()
	return pluginAppError("DeletePreferencesForUser", preferences.New(a.db).Delete(ctx, userID, prefs))
}

func (a *mattermostAPI) GetTeam(teamID string) (*mmmodel.Team, *mmmodel.AppError) {
	release, err := a.acquireGeneration()
	if err != nil {
		return nil, pluginAppError("GetTeam", err)
	}
	defer release()
	return a.getTeam(teamID)
}

func (a *mattermostAPI) getTeam(teamID string) (*mmmodel.Team, *mmmodel.AppError) {
	ctx, cancel := a.withContext()
	defer cancel()
	var team mmmodel.Team
	err := a.db.Pool.QueryRow(ctx, `
		SELECT id,name,display_name,type,create_at,update_at,delete_at
		FROM teams WHERE id=$1
	`, teamID).Scan(&team.Id, &team.Name, &team.DisplayName, &team.Type, &team.CreateAt, &team.UpdateAt, &team.DeleteAt)
	return &team, pluginAppError("GetTeam", err)
}

func (a *mattermostAPI) GetTeamsForUser(userID string) ([]*mmmodel.Team, *mmmodel.AppError) {
	release, err := a.acquireGeneration()
	if err != nil {
		return nil, pluginAppError("GetTeamsForUser", err)
	}
	defer release()
	ctx, cancel := a.withContext()
	defer cancel()
	rows, queryErr := a.db.Pool.Query(ctx, `
		SELECT t.id,t.name,t.display_name,t.type,t.create_at,t.update_at,t.delete_at
		FROM teams t JOIN team_members tm ON tm.team_id=t.id
		WHERE tm.user_id=$1 AND t.delete_at=0 ORDER BY t.create_at
	`, userID)
	if queryErr != nil {
		return nil, pluginAppError("GetTeamsForUser", queryErr)
	}
	defer rows.Close()
	result := []*mmmodel.Team{}
	for rows.Next() {
		var team mmmodel.Team
		if scanErr := rows.Scan(&team.Id, &team.Name, &team.DisplayName, &team.Type, &team.CreateAt, &team.UpdateAt, &team.DeleteAt); scanErr != nil {
			return nil, pluginAppError("GetTeamsForUser", scanErr)
		}
		result = append(result, &team)
	}
	return result, pluginAppError("GetTeamsForUser", rows.Err())
}

func (a *mattermostAPI) GetChannelsForTeamForUser(teamID, userID string, includeDeleted bool) ([]*mmmodel.Channel, *mmmodel.AppError) {
	release, err := a.acquireGeneration()
	if err != nil {
		return nil, pluginAppError("GetChannelsForTeamForUser", err)
	}
	defer release()
	ctx, cancel := a.withContext()
	defer cancel()
	query := `
		SELECT DISTINCT c.id,COALESCE(c.team_id,''),c.type,c.display_name,c.name,c.header,c.purpose,c.create_at,c.update_at,c.delete_at
		FROM channels c JOIN channel_members cm ON cm.channel_id=c.id
		WHERE cm.user_id=$1 AND (c.team_id=$2 OR c.type IN ('D','G'))`
	if !includeDeleted {
		query += ` AND c.delete_at=0`
	}
	query += ` ORDER BY c.create_at`
	rows, queryErr := a.db.Pool.Query(ctx, query, userID, teamID)
	if queryErr != nil {
		return nil, pluginAppError("GetChannelsForTeamForUser", queryErr)
	}
	defer rows.Close()
	result := []*mmmodel.Channel{}
	for rows.Next() {
		var channel channels.Channel
		if scanErr := rows.Scan(&channel.ID, &channel.TeamID, &channel.Type, &channel.DisplayName, &channel.Name, &channel.Header, &channel.Purpose, &channel.CreateAt, &channel.UpdateAt, &channel.DeleteAt); scanErr != nil {
			return nil, pluginAppError("GetChannelsForTeamForUser", scanErr)
		}
		result = append(result, toMattermostChannel(&channel))
	}
	return result, pluginAppError("GetChannelsForTeamForUser", rows.Err())
}

func (a *mattermostAPI) GetDirectChannel(userID1, userID2 string) (*mmmodel.Channel, *mmmodel.AppError) {
	release, err := a.acquireGeneration()
	if err != nil {
		return nil, pluginAppError("GetDirectChannel", err)
	}
	defer release()
	ctx, cancel := a.withContext()
	defer cancel()
	channel, ensureErr := channels.New(a.db).EnsureDirect(ctx, userID1, userID2)
	return toMattermostChannel(channel), pluginAppError("GetDirectChannel", ensureErr)
}

func (a *mattermostAPI) AddUserToChannel(channelID, userID, _ string) (*mmmodel.ChannelMember, *mmmodel.AppError) {
	release, err := a.acquireGeneration()
	if err != nil {
		return nil, pluginAppError("AddUserToChannel", err)
	}
	defer release()
	ctx, cancel := a.withContext()
	defer cancel()
	service := channels.New(a.db)
	if joinErr := service.Join(ctx, channelID, userID); joinErr != nil {
		return nil, pluginAppError("AddUserToChannel", joinErr)
	}
	member, getErr := service.GetMember(ctx, channelID, userID)
	if getErr != nil || member == nil {
		if getErr == nil {
			getErr = pgx.ErrNoRows
		}
		return nil, pluginAppError("AddUserToChannel", getErr)
	}
	return &mmmodel.ChannelMember{ChannelId: member.ChannelID, UserId: member.UserID, Roles: member.Roles, LastViewedAt: member.LastViewedAt}, nil
}

func (a *mattermostAPI) SearchPostsInTeam(teamID string, paramsList []*mmmodel.SearchParams) ([]*mmmodel.Post, *mmmodel.AppError) {
	release, err := a.acquireGeneration()
	if err != nil {
		return nil, pluginAppError("SearchPostsInTeam", err)
	}
	defer release()
	if len(paramsList) == 0 {
		paramsList = []*mmmodel.SearchParams{{}}
	}
	ctx, cancel := a.withContext()
	defer cancel()
	byID := map[string]*mmmodel.Post{}
	for _, params := range paramsList {
		if params == nil {
			params = &mmmodel.SearchParams{}
		}
		where := []string{"c.team_id=$1", "p.delete_at=0"}
		args := []any{teamID}
		addArg := func(value any) string {
			args = append(args, value)
			return fmt.Sprintf("$%d", len(args))
		}
		if terms := strings.TrimSpace(params.Terms); terms != "" {
			where = append(where, "POSITION(LOWER("+addArg(terms)+") IN LOWER(p.message))>0")
		}
		if len(params.FromUsers) > 0 {
			names := make([]string, 0, len(params.FromUsers))
			for _, name := range params.FromUsers {
				names = append(names, strings.ToLower(strings.TrimSpace(name)))
			}
			where = append(where, "LOWER(u.username)=ANY("+addArg(names)+")")
		}
		if date := strings.TrimSpace(params.OnDate); date != "" {
			parsed, parseErr := time.Parse("2006-01-02", date)
			if parseErr != nil {
				return nil, pluginAppError("SearchPostsInTeam", parseErr)
			}
			start := parsed.Add(-time.Duration(params.TimeZoneOffset) * time.Second).UnixMilli()
			where = append(where, "p.create_at>="+addArg(start), "p.create_at<"+addArg(start+int64(24*time.Hour/time.Millisecond)))
		}
		rows, queryErr := a.db.Pool.Query(ctx, `
			SELECT p.id,p.channel_id,p.user_id,p.root_id,p.message,p.props,p.file_ids,p.is_pinned,p.create_at,p.update_at,p.delete_at,p.link_metadata
			FROM posts p JOIN channels c ON c.id=p.channel_id JOIN users u ON u.id=p.user_id
			WHERE `+strings.Join(where, " AND ")+` ORDER BY p.create_at DESC
		`, args...)
		if queryErr != nil {
			return nil, pluginAppError("SearchPostsInTeam", queryErr)
		}
		for rows.Next() {
			post, scanErr := scanCompatPost(rows)
			if scanErr != nil {
				rows.Close()
				return nil, pluginAppError("SearchPostsInTeam", scanErr)
			}
			byID[post.Id] = post
		}
		rowsErr := rows.Err()
		rows.Close()
		if rowsErr != nil {
			return nil, pluginAppError("SearchPostsInTeam", rowsErr)
		}
	}
	result := make([]*mmmodel.Post, 0, len(byID))
	for _, post := range byID {
		result = append(result, post)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreateAt == result[j].CreateAt {
			return result[i].Id < result[j].Id
		}
		return result[i].CreateAt > result[j].CreateAt
	})
	return result, nil
}

func scanCompatPost(row userScanner) (*mmmodel.Post, error) {
	var post posts.Post
	var propsRaw, fileIDsRaw, linkRaw []byte
	err := row.Scan(&post.ID, &post.ChannelID, &post.UserID, &post.RootID, &post.Message, &propsRaw, &fileIDsRaw, &post.IsPinned, &post.CreateAt, &post.UpdateAt, &post.DeleteAt, &linkRaw)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(propsRaw, &post.Props)
	_ = json.Unmarshal(fileIDsRaw, &post.FileIDs)
	if value, ok := post.Props["_moyro_post_type"].(string); ok {
		post.Type = value
	}
	return toMattermostPost(&post), nil
}

func (a *mattermostAPI) CreatePost(post *mmmodel.Post) (*mmmodel.Post, *mmmodel.AppError) {
	release, err := a.acquireGeneration()
	if err != nil {
		return nil, pluginAppError("CreatePost", err)
	}
	defer release()
	if post == nil {
		return nil, pluginAppError("CreatePost", errors.New("post is required"))
	}
	a.host.mu.RLock()
	service := a.host.postCommands
	a.host.mu.RUnlock()
	if service == nil {
		return nil, pluginAppError("CreatePost", errors.New("plugin post service is unavailable"))
	}
	ctx, cancel := a.withContext()
	defer cancel()
	created, createErr := service.Execute(ctx, postcommand.Command{
		Source: postcommand.SourcePlugin, ActorID: post.UserId,
		ChannelID: post.ChannelId, RootID: post.RootId, Message: post.Message,
		Props: post.GetProps(), FileIDs: append([]string(nil), post.FileIds...),
		PluginID: a.pluginID, PostType: post.Type,
	})
	if createErr != nil {
		return nil, pluginAppError("CreatePost", createErr)
	}
	return toMattermostPost(created), nil
}

func (a *mattermostAPI) UpdatePost(post *mmmodel.Post) (*mmmodel.Post, *mmmodel.AppError) {
	release, err := a.acquireGeneration()
	if err != nil {
		return nil, pluginAppError("UpdatePost", err)
	}
	defer release()
	if post == nil || post.Id == "" {
		return nil, pluginAppError("UpdatePost", errors.New("post id is required"))
	}
	ctx, cancel := a.withContext()
	defer cancel()
	props := map[string]any{}
	for key, value := range post.GetProps() {
		if key == "plugin_id" || key == "from_plugin" || strings.HasPrefix(key, "_moyro_") {
			continue
		}
		props[key] = value
	}
	props["from_plugin"] = true
	props["plugin_id"] = a.pluginID
	if post.Type != "" {
		props["_moyro_post_type"] = post.Type
	}
	updated, updateErr := posts.New(a.db).Update(ctx, post.Id, post.UserId, post.Message, props)
	if updateErr != nil {
		return nil, pluginAppError("UpdatePost", updateErr)
	}
	if updated == nil {
		return nil, pluginAppError("UpdatePost", pgx.ErrNoRows)
	}
	updated.Type = post.Type
	raw, _ := json.Marshal(updated)
	a.host.mu.RLock()
	events, auditSink := a.host.events, a.host.audit
	a.host.mu.RUnlock()
	if events != nil {
		events.Broadcast(ws.Event{Event: "post_edited", Data: map[string]any{"post": string(raw), "channel_id": updated.ChannelID}, Broadcast: ws.Broadcast{ChannelID: updated.ChannelID}})
	}
	if auditSink != nil {
		auditSink.LogAsync(post.UserId, audit.ActionPostRewrite, post.Id, map[string]any{"source": string(postcommand.SourcePlugin), "plugin_id": a.pluginID, "channel_id": updated.ChannelID})
	}
	return toMattermostPost(updated), nil
}

func (a *mattermostAPI) SendEphemeralPost(userID string, post *mmmodel.Post) *mmmodel.Post {
	release, err := a.acquireGeneration()
	if err != nil || post == nil {
		return nil
	}
	defer release()
	copy := post.Clone()
	if copy.Id == "" {
		copy.Id = uuid.NewString()
	}
	now := time.Now().UnixMilli()
	if copy.CreateAt == 0 {
		copy.CreateAt = now
	}
	copy.UpdateAt = now
	props := copy.GetProps()
	if props == nil {
		props = map[string]any{}
	}
	props["from_plugin"] = true
	props["plugin_id"] = a.pluginID
	copy.SetProps(props)
	raw, _ := json.Marshal(copy)
	a.host.mu.RLock()
	events, auditSink := a.host.events, a.host.audit
	a.host.mu.RUnlock()
	if events != nil {
		events.Broadcast(ws.Event{Event: "ephemeral_message", Data: map[string]any{"post": string(raw)}, Broadcast: ws.Broadcast{UserID: userID}})
	}
	if auditSink != nil {
		auditSink.LogAsync(copy.UserId, audit.ActionPostEphemeral, userID, map[string]any{"source": string(postcommand.SourcePlugin), "plugin_id": a.pluginID, "channel_id": copy.ChannelId})
	}
	return copy
}

func (a *mattermostAPI) GetPost(postID string) (*mmmodel.Post, *mmmodel.AppError) {
	release, err := a.acquireGeneration()
	if err != nil {
		return nil, pluginAppError("GetPost", err)
	}
	defer release()
	ctx, cancel := a.withContext()
	defer cancel()
	post, getErr := posts.New(a.db).Get(ctx, postID)
	return toMattermostPost(post), pluginAppError("GetPost", getErr)
}

func (a *mattermostAPI) GetPostThread(postID string) (*mmmodel.PostList, *mmmodel.AppError) {
	return a.getPostList("GetPostThread", func(ctx context.Context, service *posts.Service) (*posts.PostList, error) {
		return service.ListThread(ctx, postID)
	})
}

func (a *mattermostAPI) GetPostsSince(channelID string, since int64) (*mmmodel.PostList, *mmmodel.AppError) {
	release, err := a.acquireGeneration()
	if err != nil {
		return nil, pluginAppError("GetPostsSince", err)
	}
	defer release()
	ctx, cancel := a.withContext()
	defer cancel()
	rows, queryErr := a.db.Pool.Query(ctx, `
		SELECT id,channel_id,user_id,root_id,message,props,file_ids,is_pinned,create_at,update_at,delete_at,link_metadata
		FROM posts WHERE channel_id=$1 AND delete_at=0 AND create_at >= $2
		ORDER BY create_at ASC
	`, channelID, since)
	if queryErr != nil {
		return nil, pluginAppError("GetPostsSince", queryErr)
	}
	defer rows.Close()
	result := mmmodel.NewPostList()
	for rows.Next() {
		post, scanErr := scanCompatPost(rows)
		if scanErr != nil {
			return nil, pluginAppError("GetPostsSince", scanErr)
		}
		result.Posts[post.Id] = post
		result.Order = append(result.Order, post.Id)
	}
	return result, pluginAppError("GetPostsSince", rows.Err())
}

func (a *mattermostAPI) GetPostsAfter(channelID, postID string, page, perPage int) (*mmmodel.PostList, *mmmodel.AppError) {
	return a.getPostList("GetPostsAfter", func(ctx context.Context, service *posts.Service) (*posts.PostList, error) {
		return service.ListForChannelPaged(ctx, channelID, posts.PageOpts{After: postID, Page: page, PerPage: perPage})
	})
}

func (a *mattermostAPI) GetPostsBefore(channelID, postID string, page, perPage int) (*mmmodel.PostList, *mmmodel.AppError) {
	return a.getPostList("GetPostsBefore", func(ctx context.Context, service *posts.Service) (*posts.PostList, error) {
		return service.ListForChannelPaged(ctx, channelID, posts.PageOpts{Before: postID, Page: page, PerPage: perPage})
	})
}

func (a *mattermostAPI) getPostList(where string, load func(context.Context, *posts.Service) (*posts.PostList, error)) (*mmmodel.PostList, *mmmodel.AppError) {
	release, err := a.acquireGeneration()
	if err != nil {
		return nil, pluginAppError(where, err)
	}
	defer release()
	ctx, cancel := a.withContext()
	defer cancel()
	list, loadErr := load(ctx, posts.New(a.db))
	return toMattermostPostList(list), pluginAppError(where, loadErr)
}

func (a *mattermostAPI) GetFileInfo(fileID string) (*mmmodel.FileInfo, *mmmodel.AppError) {
	release, err := a.acquireGeneration()
	if err != nil {
		return nil, pluginAppError("GetFileInfo", err)
	}
	defer release()
	a.host.mu.RLock()
	service := a.host.files
	a.host.mu.RUnlock()
	if service == nil {
		return nil, pluginAppError("GetFileInfo", errors.New("plugin file service is unavailable"))
	}
	ctx, cancel := a.withContext()
	defer cancel()
	info, getErr := service.GetInfo(ctx, fileID)
	if getErr != nil {
		return nil, pluginAppError("GetFileInfo", getErr)
	}
	return &mmmodel.FileInfo{Id: info.ID, CreatorId: info.UserID, PostId: info.PostID, ChannelId: info.ChannelID, Name: info.Name, Extension: info.Extension, Size: info.Size, MimeType: info.MimeType, Width: info.Width, Height: info.Height, HasPreviewImage: info.HasThumbnail, CreateAt: info.CreateAt, UpdateAt: info.UpdateAt, DeleteAt: info.DeleteAt}, nil
}

func (a *mattermostAPI) GetFile(fileID string) ([]byte, *mmmodel.AppError) {
	release, err := a.acquireGeneration()
	if err != nil {
		return nil, pluginAppError("GetFile", err)
	}
	defer release()
	a.host.mu.RLock()
	service := a.host.files
	a.host.mu.RUnlock()
	if service == nil {
		return nil, pluginAppError("GetFile", errors.New("plugin file service is unavailable"))
	}
	ctx, cancel := a.withContext()
	defer cancel()
	reader, _, openErr := service.Open(ctx, fileID)
	if openErr != nil {
		return nil, pluginAppError("GetFile", openErr)
	}
	defer reader.Close()
	data, readErr := io.ReadAll(io.LimitReader(reader, 101*1024*1024))
	if readErr == nil && len(data) > 100*1024*1024 {
		readErr = errors.New("plugin file read exceeds 100 MiB")
	}
	return data, pluginAppError("GetFile", readErr)
}

func (a *mattermostAPI) PublishWebSocketEvent(event string, payload map[string]any, broadcast *mmmodel.WebsocketBroadcast) {
	release, err := a.acquireGeneration()
	if err != nil {
		return
	}
	defer release()
	a.host.mu.RLock()
	events := a.host.events
	a.host.mu.RUnlock()
	if events == nil {
		return
	}
	audience := ws.Broadcast{}
	if broadcast != nil {
		audience.UserID, audience.ChannelID, audience.TeamID = broadcast.UserId, broadcast.ChannelId, broadcast.TeamId
		for userID, omitted := range broadcast.OmitUsers {
			if omitted {
				audience.OmitUsers = append(audience.OmitUsers, userID)
			}
		}
	}
	events.Broadcast(ws.Event{Event: "custom_" + a.pluginID + "_" + strings.TrimSpace(event), Data: payload, Broadcast: audience})
}

func (a *mattermostAPI) HasPermissionToChannel(userID, channelID string, permission *mmmodel.Permission) bool {
	release, err := a.acquireGeneration()
	if err != nil || permission == nil {
		return false
	}
	defer release()
	ctx, cancel := a.withContext()
	defer cancel()
	var roles string
	if queryErr := a.db.Pool.QueryRow(ctx, `SELECT roles FROM users WHERE id=$1 AND delete_at=0`, userID).Scan(&roles); queryErr != nil {
		return false
	}
	if strings.Contains(" "+roles+" ", " system_admin ") {
		return true
	}
	var channelRoles string
	if queryErr := a.db.Pool.QueryRow(ctx, `SELECT roles FROM channel_members WHERE channel_id=$1 AND user_id=$2`, channelID, userID).Scan(&channelRoles); queryErr != nil {
		return false
	}
	switch permission.Id {
	case mmmodel.PermissionReadChannel.Id, mmmodel.PermissionCreatePost.Id:
		return true
	default:
		return strings.Contains(" "+channelRoles+" ", " channel_admin ")
	}
}

func (a *mattermostAPI) CreateBot(bot *mmmodel.Bot) (*mmmodel.Bot, *mmmodel.AppError) {
	release, err := a.acquireGeneration()
	if err != nil {
		return nil, pluginAppError("CreateBot", err)
	}
	defer release()
	if bot == nil {
		return nil, pluginAppError("CreateBot", errors.New("bot is required"))
	}
	ctx, cancel := a.withContext()
	defer cancel()
	created, createErr := a.createPluginBot(ctx, bot)
	if createErr != nil {
		return nil, pluginAppError("CreateBot", createErr)
	}
	return created, nil
}

func (a *mattermostAPI) PatchBot(botUserID string, patch *mmmodel.BotPatch) (*mmmodel.Bot, *mmmodel.AppError) {
	release, err := a.acquireGeneration()
	if err != nil {
		return nil, pluginAppError("PatchBot", err)
	}
	defer release()
	ctx, cancel := a.withContext()
	defer cancel()
	current, getErr := a.getOwnedBot(ctx, botUserID, true)
	if getErr != nil {
		return nil, pluginAppError("PatchBot", getErr)
	}
	if patch != nil {
		if patch.Username != nil {
			username := strings.ToLower(strings.TrimSpace(*patch.Username))
			if _, err := a.db.Pool.Exec(ctx, `UPDATE users SET username=$2,email=$3,update_at=$4 WHERE id=$1`, botUserID, username, "bot+"+username+"@localhost", time.Now().UnixMilli()); err != nil {
				return nil, pluginAppError("PatchBot", err)
			}
			current.Username = username
		}
		display, description := current.DisplayName, current.Description
		if patch.DisplayName != nil {
			display = *patch.DisplayName
		}
		if patch.Description != nil {
			description = *patch.Description
		}
		now := time.Now().UnixMilli()
		if _, updateErr := a.db.Pool.Exec(ctx, `UPDATE users SET nickname=$2,bot_description=$3,update_at=$4 WHERE id=$1`, botUserID, display, description, now); updateErr != nil {
			return nil, pluginAppError("PatchBot", updateErr)
		}
		current, getErr = a.getOwnedBot(ctx, botUserID, true)
		if getErr != nil {
			return nil, pluginAppError("PatchBot", getErr)
		}
	}
	return current, nil
}

func (a *mattermostAPI) GetBot(botUserID string, includeDeleted bool) (*mmmodel.Bot, *mmmodel.AppError) {
	release, err := a.acquireGeneration()
	if err != nil {
		return nil, pluginAppError("GetBot", err)
	}
	defer release()
	ctx, cancel := a.withContext()
	defer cancel()
	bot, getErr := a.getOwnedBot(ctx, botUserID, includeDeleted)
	return bot, pluginAppError("GetBot", getErr)
}

func (a *mattermostAPI) GetBots(options *mmmodel.BotGetOptions) ([]*mmmodel.Bot, *mmmodel.AppError) {
	release, err := a.acquireGeneration()
	if err != nil {
		return nil, pluginAppError("GetBots", err)
	}
	defer release()
	if options == nil {
		options = &mmmodel.BotGetOptions{}
	}
	page, perPage := options.Page, options.PerPage
	if page < 0 {
		page = 0
	}
	if perPage <= 0 || perPage > 200 {
		perPage = 60
	}
	ownerID := options.OwnerId
	if ownerID == "" {
		ownerID = a.pluginID
	}
	if ownerID != a.pluginID {
		return []*mmmodel.Bot{}, nil
	}
	ctx, cancel := a.withContext()
	defer cancel()
	query := `
		SELECT id,username,COALESCE(nickname,''),COALESCE(bot_description,''),COALESCE(plugin_owner_id,''),create_at,update_at,delete_at
		FROM users WHERE COALESCE(is_bot,FALSE)=TRUE AND COALESCE(plugin_owner_id,'')=$1`
	if !options.IncludeDeleted {
		query += ` AND delete_at=0`
	}
	query += ` ORDER BY username LIMIT $2 OFFSET $3`
	rows, queryErr := a.db.Pool.Query(ctx, query, ownerID, perPage, page*perPage)
	if queryErr != nil {
		return nil, pluginAppError("GetBots", queryErr)
	}
	defer rows.Close()
	result := []*mmmodel.Bot{}
	for rows.Next() {
		var bot mmmodel.Bot
		if scanErr := rows.Scan(&bot.UserId, &bot.Username, &bot.DisplayName, &bot.Description, &bot.OwnerId, &bot.CreateAt, &bot.UpdateAt, &bot.DeleteAt); scanErr != nil {
			return nil, pluginAppError("GetBots", scanErr)
		}
		result = append(result, &bot)
	}
	return result, pluginAppError("GetBots", rows.Err())
}

func (a *mattermostAPI) UpdateBotActive(botUserID string, active bool) (*mmmodel.Bot, *mmmodel.AppError) {
	release, err := a.acquireGeneration()
	if err != nil {
		return nil, pluginAppError("UpdateBotActive", err)
	}
	defer release()
	ctx, cancel := a.withContext()
	defer cancel()
	if _, getErr := a.getOwnedBot(ctx, botUserID, true); getErr != nil {
		return nil, pluginAppError("UpdateBotActive", getErr)
	}
	now, deleteAt := time.Now().UnixMilli(), int64(0)
	if !active {
		deleteAt = now
	}
	_, updateErr := a.db.Pool.Exec(ctx, `UPDATE users SET delete_at=$2,update_at=$3 WHERE id=$1`, botUserID, deleteAt, now)
	if updateErr != nil {
		return nil, pluginAppError("UpdateBotActive", updateErr)
	}
	bot, getErr := a.getOwnedBot(ctx, botUserID, true)
	return bot, pluginAppError("UpdateBotActive", getErr)
}

func (a *mattermostAPI) EnsureBotUser(bot *mmmodel.Bot) (string, error) {
	release, err := a.acquireGeneration()
	if err != nil {
		return "", err
	}
	defer release()
	if bot == nil || strings.TrimSpace(bot.Username) == "" {
		return "", errors.New("bot username is required")
	}
	ctx, cancel := a.withContext()
	defer cancel()
	var userID string
	var isBot bool
	var ownerID string
	lookupErr := a.db.Pool.QueryRow(ctx, `SELECT id,COALESCE(is_bot,FALSE),COALESCE(plugin_owner_id,'') FROM users WHERE LOWER(username)=LOWER($1)`, bot.Username).Scan(&userID, &isBot, &ownerID)
	if lookupErr == nil {
		if !isBot || ownerID != a.pluginID {
			return "", fmt.Errorf("username %q is not an owned bot", bot.Username)
		}
		now := time.Now().UnixMilli()
		_, updateErr := a.db.Pool.Exec(ctx, `UPDATE users SET nickname=$2,bot_description=$3,delete_at=0,update_at=$4 WHERE id=$1`, userID, bot.DisplayName, bot.Description, now)
		return userID, updateErr
	}
	if !errors.Is(lookupErr, pgx.ErrNoRows) {
		return "", lookupErr
	}
	created, appErr := a.createBotWithoutLease(ctx, bot)
	if appErr != nil {
		return "", appErr
	}
	return created.UserId, nil
}

func (a *mattermostAPI) createBotWithoutLease(ctx context.Context, bot *mmmodel.Bot) (*mmmodel.Bot, error) {
	return a.createPluginBot(ctx, bot)
}

func (a *mattermostAPI) createPluginBot(ctx context.Context, bot *mmmodel.Bot) (*mmmodel.Bot, error) {
	username := strings.ToLower(strings.TrimSpace(bot.Username))
	if username == "" {
		return nil, errors.New("bot username is required")
	}
	id, now := uuid.NewString(), time.Now().UnixMilli()
	email := "plugin-bot+" + strings.ReplaceAll(id, "-", "") + "@localhost.invalid"
	_, err := a.db.Pool.Exec(ctx, `
		INSERT INTO users
			(id,username,email,password_hash,roles,create_at,update_at,is_bot,
			 plugin_owner_id,bot_description,nickname)
		VALUES ($1,$2,$3,'','system_user',$4,$4,TRUE,$5,$6,$7)
	`, id, username, email, now, a.pluginID, bot.Description, bot.DisplayName)
	if err != nil {
		return nil, err
	}
	return &mmmodel.Bot{UserId: id, Username: username, DisplayName: bot.DisplayName, Description: bot.Description, OwnerId: a.pluginID, CreateAt: now, UpdateAt: now}, nil
}

func (a *mattermostAPI) getOwnedBot(ctx context.Context, botUserID string, includeDeleted bool) (*mmmodel.Bot, error) {
	query := `
		SELECT id,username,COALESCE(nickname,''),COALESCE(bot_description,''),COALESCE(plugin_owner_id,''),create_at,update_at,delete_at
		FROM users WHERE id=$1 AND COALESCE(is_bot,FALSE)=TRUE AND COALESCE(plugin_owner_id,'')=$2`
	if !includeDeleted {
		query += ` AND delete_at=0`
	}
	var bot mmmodel.Bot
	err := a.db.Pool.QueryRow(ctx, query, botUserID, a.pluginID).Scan(&bot.UserId, &bot.Username, &bot.DisplayName, &bot.Description, &bot.OwnerId, &bot.CreateAt, &bot.UpdateAt, &bot.DeleteAt)
	return &bot, err
}
