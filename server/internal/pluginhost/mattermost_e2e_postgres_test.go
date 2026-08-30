package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hkjang/moyro/server/internal/application/postcommand"
	"github.com/hkjang/moyro/server/internal/audit"
	"github.com/hkjang/moyro/server/internal/auth"
	"github.com/hkjang/moyro/server/internal/channels"
	"github.com/hkjang/moyro/server/internal/files"
	"github.com/hkjang/moyro/server/internal/posts"
	"github.com/hkjang/moyro/server/internal/secrets"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/hkjang/moyro/server/internal/ws"
)

const pluginRuntimeTestPostgresDSN = "MOYRO_TEST_POSTGRES_DSN"

func TestMattermostArchivesInstallRunAndSurviveRestart(t *testing.T) {
	db := newPluginRuntimeTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate plugin test schema: %v", err)
	}
	seedPluginRuntimeData(t, ctx, db)

	botmanArchive := pluginArchivePath(t, "MOYRO_TEST_BOTMAN_PLUGIN_ARCHIVE", "mattermost-botman-plugin/dist/com.mattermost.botman-0.1.2.tar.gz")
	chatdumpArchive := pluginArchivePath(t, "MOYRO_TEST_CHATDUMP_PLUGIN_ARCHIVE", "mattermost-chatdump-plugin/dist-release-check/com.hkjang.mattermost-chatdump-plugin-0.5.1.tar.gz")
	langflowArchive := pluginArchivePath(t, "MOYRO_TEST_LANGFLOW_PLUGIN_ARCHIVE", "mattermost-langflow-plugin/dist/com.mattermost.langflow-0.1.20.tar.gz")
	echoArchive := pluginArchivePath(t, "MOYRO_TEST_ECHOSUMMARY_PLUGIN_ARCHIVE", "mattermost-echosummary-plugin/dist/com.mattermost.echosummary-0.6.5.tar.gz")
	manager, err := secrets.New(bytes.Repeat([]byte{0x7b}, secrets.MasterKeySize))
	if err != nil {
		t.Fatalf("create plugin test secret manager: %v", err)
	}
	pluginDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	host, err := NewWithRuntime(pluginDir, db, manager, logger)
	if err != nil {
		t.Fatalf("create plugin runtime: %v", err)
	}
	events := &pluginTestEventSink{}
	postCommands := bindPluginTestApplication(t, db, host, events, logger)
	hostRunning := true
	t.Cleanup(func() {
		if hostRunning {
			host.Shutdown()
		}
	})

	botman := installPluginArchive(t, ctx, host, botmanArchive)
	if botman.ID != "com.mattermost.botman" || botman.State != "running" || botman.SHA256 == "" {
		t.Fatalf("botman install = %#v", botman)
	}
	chatdump := installPluginArchive(t, ctx, host, chatdumpArchive)
	if chatdump.ID != "com.hkjang.mattermost-chatdump-plugin" || chatdump.State != "running" || chatdump.SHA256 == "" {
		t.Fatalf("chatdump install = %#v", chatdump)
	}
	langflow := installPluginArchive(t, ctx, host, langflowArchive)
	if langflow.ID != "com.mattermost.langflow" || langflow.State != "running" || langflow.SHA256 == "" {
		t.Fatalf("langflow install = %#v", langflow)
	}
	echo := installPluginArchive(t, ctx, host, echoArchive)
	if echo.ID != "com.mattermost.echosummary" || echo.State != "running" || echo.SHA256 == "" {
		t.Fatalf("echo summary install = %#v", echo)
	}
	if got := len(host.WebappBundles()); got != 4 {
		t.Fatalf("webapp bundles = %d, want 4", got)
	}

	botStatus := pluginRequest(t, host, botman.ID, "admin-user", "/api/v1/status")
	if botStatus.Code != http.StatusOK {
		t.Fatalf("botman status = %d: %s", botStatus.Code, botStatus.Body.String())
	}
	chatSettings := pluginRequest(t, host, chatdump.ID, "admin-user", "/api/v1/export/settings")
	if chatSettings.Code != http.StatusOK || !strings.Contains(chatSettings.Body.String(), `"enabled":true`) {
		t.Fatalf("chatdump settings = %d: %s", chatSettings.Code, chatSettings.Body.String())
	}
	export := pluginRequest(t, host, chatdump.ID, "admin-user", "/api/v1/export/download?channel_id=channel-1&weeks=1&format=json")
	if export.Code != http.StatusOK || !strings.Contains(export.Body.String(), "plugin compatibility message") {
		t.Fatalf("chatdump export = %d: %s", export.Code, export.Body.String())
	}
	langflowStatus := pluginRequest(t, host, langflow.ID, "admin-user", "/api/v1/status")
	if langflowStatus.Code != http.StatusOK || !strings.Contains(langflowStatus.Body.String(), `"plugin_id":"com.mattermost.langflow"`) {
		t.Fatalf("langflow status = %d: %s", langflowStatus.Code, langflowStatus.Body.String())
	}
	echoReply, handled, commandErr := host.ExecuteCommand(ctx, "echosummary", "status", "channel-1", "admin-user")
	if commandErr != nil || !handled || echoReply == nil || !strings.Contains(echoReply.Text, "vLLM") {
		t.Fatalf("echo summary status command = reply %#v handled %v err %v", echoReply, handled, commandErr)
	}
	if reply, handled, commandErr := host.ExecuteCommand(ctx, "not-echosummary", "status", "channel-1", "admin-user"); commandErr != nil || handled || reply != nil {
		t.Fatalf("unregistered command leaked to plugin: reply %#v handled %v err %v", reply, handled, commandErr)
	}

	var langflowCalls atomic.Int32
	langflowServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		langflowCalls.Add(1)
		if r.Method != http.MethodPost || !strings.Contains(r.URL.Path, "/api/v1/run/flow-e2e") || r.URL.Query().Get("stream") != "true" {
			http.Error(w, "unexpected Langflow request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"event\":\"token\",\"data\":{\"chunk\":\"Langflow compatibility\"}}\n\n")
		_, _ = io.WriteString(w, "data: {\"event\":\"end\",\"data\":{\"text\":\"Langflow compatibility works\"}}\n\n")
	}))
	defer langflowServer.Close()
	langflowConfig, _ := json.Marshal(map[string]any{
		"service": map[string]any{"base_url": langflowServer.URL, "auth_mode": "bearer", "allow_hosts": "127.0.0.1"},
		"runtime": map[string]any{
			"default_timeout_seconds": 5, "enable_streaming": true, "streaming_update_ms": 1,
			"max_input_length": 4000, "max_output_length": 8000, "context_post_limit": 8,
		},
		"bots": []any{map[string]any{
			"id": "flow-e2e", "username": "flowbot", "display_name": "Flow Bot",
			"description": "Moyro plugin compatibility bot", "flow_id": "flow-e2e",
		}},
	})
	if err := host.UpdateConfiguration(ctx, langflow.ID, map[string]any{"Config": string(langflowConfig)}); err != nil {
		t.Fatalf("configure Langflow plugin: %v", err)
	}
	trigger, err := postCommands.Execute(ctx, postcommand.Command{
		Source: postcommand.SourceREST, ActorID: "admin-user", ChannelID: "channel-1",
		Message: "@flowbot verify Moyro compatibility",
	})
	if err != nil {
		t.Fatalf("create Langflow trigger post: %v", err)
	}
	langflowPost := waitForPluginPost(t, db, langflow.ID, "Langflow compatibility works")
	if langflowPost.rootID != trigger.ID || langflowPost.postType != "custom_langflow_bot" || langflowPost.streamStatus != "completed" {
		t.Fatalf("Langflow response metadata = %#v, trigger %s", langflowPost, trigger.ID)
	}
	if langflowCalls.Load() == 0 {
		t.Fatal("Langflow mock provider was not called")
	}
	if !events.contains("custom_com.mattermost.langflow_postupdate") {
		t.Fatal("Langflow streaming websocket event was not published")
	}
	history := waitForPluginHTTP(t, host, langflow.ID, "admin-user", "/api/v1/history?limit=5", `"status":"completed"`)
	if history.Code != http.StatusOK {
		t.Fatalf("Langflow history = %d: %s", history.Code, history.Body.String())
	}

	var vllmCalls atomic.Int32
	vllmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vllmCalls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "unexpected vLLM request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"Echo compatibility summary complete"}}]}`)
	}))
	defer vllmServer.Close()
	if err := host.UpdateConfiguration(ctx, echo.ID, map[string]any{
		"VLLMBaseURL": vllmServer.URL, "VLLMModel": "mock-summary-model",
		"NotificationTimezone": "Asia/Seoul", "RequestTimeoutSeconds": 5,
		"ContextMessagesBefore": 2, "ContextMessagesAfter": 2,
	}); err != nil {
		t.Fatalf("configure Echo Summary plugin: %v", err)
	}
	echoNow, handled, commandErr := host.ExecuteCommand(ctx, "echosummary", "now", "channel-1", "admin-user")
	if commandErr != nil || !handled || echoNow == nil || !strings.Contains(echoNow.Text, "접수") {
		t.Fatalf("Echo Summary now command = reply %#v handled %v err %v", echoNow, handled, commandErr)
	}
	echoPost := waitForPluginPost(t, db, echo.ID, "Echo compatibility summary complete")
	if echoPost.channelType != "D" {
		t.Fatalf("Echo Summary result channel type = %q, want D", echoPost.channelType)
	}
	if vllmCalls.Load() == 0 {
		t.Fatal("vLLM mock provider was not called")
	}

	secretMarker := "moyro-plugin-secret-marker"
	configJSON, _ := json.Marshal(map[string]any{
		"monitoring": map[string]any{"response_window_seconds": 120, "top_n": 5, "event_ingestion_token": secretMarker},
		"bots":       []any{},
	})
	if err := host.UpdateConfiguration(ctx, botman.ID, map[string]any{"Config": string(configJSON)}); err != nil {
		t.Fatalf("update encrypted botman configuration: %v", err)
	}
	var ciphertext []byte
	if err := db.Pool.QueryRow(ctx, `SELECT config_ciphertext FROM plugins WHERE id=$1`, botman.ID).Scan(&ciphertext); err != nil {
		t.Fatalf("read encrypted plugin configuration: %v", err)
	}
	if bytes.Contains(ciphertext, []byte(secretMarker)) {
		t.Fatal("plugin configuration secret was stored in plaintext")
	}
	if err := host.UpdateConfiguration(ctx, chatdump.ID, map[string]any{
		"EnableExport": false, "MaxWeeks": "12", "DefaultWeeks": "4",
		"AllowedFormats": "txt,json,csv", "DefaultFormat": "json",
	}); err != nil {
		t.Fatalf("update chatdump configuration: %v", err)
	}
	disabledSettings := pluginRequest(t, host, chatdump.ID, "admin-user", "/api/v1/export/settings")
	if disabledSettings.Code != http.StatusOK || !strings.Contains(disabledSettings.Body.String(), `"enabled":false`) {
		t.Fatalf("chatdump disabled settings = %d: %s", disabledSettings.Code, disabledSettings.Body.String())
	}
	chatdumpReplacement := replacePluginArchive(t, ctx, host, chatdumpArchive)
	if !chatdumpReplacement.Replaced || chatdumpReplacement.State != "running" {
		t.Fatalf("chatdump replacement = %#v", chatdumpReplacement)
	}
	replacedSettings := pluginRequest(t, host, chatdump.ID, "admin-user", "/api/v1/export/settings")
	if replacedSettings.Code != http.StatusOK || !strings.Contains(replacedSettings.Body.String(), `"enabled":false`) {
		t.Fatalf("chatdump settings after replacement = %d: %s", replacedSettings.Code, replacedSettings.Body.String())
	}

	host.Shutdown()
	hostRunning = false
	restarted, err := NewWithRuntime(pluginDir, db, manager, logger)
	if err != nil {
		t.Fatalf("recreate plugin runtime: %v", err)
	}
	host = restarted
	hostRunning = true
	postCommands = bindPluginTestApplication(t, db, host, events, logger)
	if err := host.LoadAll(ctx); err != nil {
		t.Fatalf("reload installed plugins: %v", err)
	}
	assertPluginState(t, host, botman.ID, "running")
	assertPluginState(t, host, chatdump.ID, "running")
	assertPluginState(t, host, langflow.ID, "running")
	assertPluginState(t, host, echo.ID, "running")

	if _, err := host.Disable(ctx, chatdump.ID); err != nil {
		t.Fatalf("disable chatdump: %v", err)
	}
	assertPluginState(t, host, chatdump.ID, "installed")
	if _, err := host.Enable(ctx, chatdump.ID); err != nil {
		t.Fatalf("re-enable chatdump: %v", err)
	}
	assertPluginState(t, host, chatdump.ID, "running")
	if _, err := host.Disable(ctx, echo.ID); err != nil {
		t.Fatalf("disable Echo Summary: %v", err)
	}
	assertPluginState(t, host, echo.ID, "installed")
	if reply, handled, commandErr := host.ExecuteCommand(ctx, "echosummary", "status", "channel-1", "admin-user"); commandErr != nil || handled || reply != nil {
		t.Fatalf("disabled Echo Summary handled command: reply %#v handled %v err %v", reply, handled, commandErr)
	}
	if _, err := host.Enable(ctx, echo.ID); err != nil {
		t.Fatalf("re-enable Echo Summary: %v", err)
	}
	assertPluginState(t, host, echo.ID, "running")
	if reply, handled, commandErr := host.ExecuteCommand(ctx, "echosummary", "status", "channel-1", "admin-user"); commandErr != nil || !handled || reply == nil {
		t.Fatalf("re-enabled Echo Summary command = reply %#v handled %v err %v", reply, handled, commandErr)
	}
	if err := host.Delete(ctx, botman.ID); err != nil {
		t.Fatalf("delete botman: %v", err)
	}
	if _, ok := host.plugin(botman.ID); ok {
		t.Fatal("deleted botman remains registered")
	}
	if _, err := os.Stat(filepath.Join(pluginDir, botman.ID)); !os.IsNotExist(err) {
		t.Fatalf("deleted botman directory stat error = %v, want not-exist", err)
	}
}

func installPluginArchive(t *testing.T, ctx context.Context, host *Host, archivePath string) *InstallResult {
	t.Helper()
	file, err := os.Open(archivePath)
	if err != nil {
		t.Fatalf("open plugin archive %s: %v", archivePath, err)
	}
	defer file.Close()
	result, err := host.Install(ctx, file, "admin-user", false)
	if err != nil {
		t.Fatalf("install plugin archive %s: %v", archivePath, err)
	}
	return result
}

func replacePluginArchive(t *testing.T, ctx context.Context, host *Host, archivePath string) *InstallResult {
	t.Helper()
	file, err := os.Open(archivePath)
	if err != nil {
		t.Fatalf("open replacement plugin archive %s: %v", archivePath, err)
	}
	defer file.Close()
	result, err := host.Install(ctx, file, "admin-user", true)
	if err != nil {
		t.Fatalf("replace plugin archive %s: %v", archivePath, err)
	}
	return result
}

type pluginTestEventSink struct {
	mu     sync.Mutex
	events []ws.Event
}

func (s *pluginTestEventSink) Broadcast(event ws.Event) {
	s.mu.Lock()
	s.events = append(s.events, event)
	s.mu.Unlock()
}

func (s *pluginTestEventSink) contains(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, event := range s.events {
		if event.Event == name {
			return true
		}
	}
	return false
}

type pluginTestUserDirectory struct{ db *store.DB }

func (d pluginTestUserDirectory) UserIDsByUsernames(ctx context.Context, names []string) (map[string]string, error) {
	result := map[string]string{}
	if len(names) == 0 {
		return result, nil
	}
	rows, err := d.db.Pool.Query(ctx, `SELECT LOWER(username),id FROM users WHERE LOWER(username)=ANY($1) AND delete_at=0`, names)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var username, id string
		if err := rows.Scan(&username, &id); err != nil {
			return nil, err
		}
		result[username] = id
	}
	return result, rows.Err()
}

func (d pluginTestUserDirectory) UserByID(ctx context.Context, id string) (*auth.User, error) {
	var user auth.User
	err := d.db.Pool.QueryRow(ctx, `SELECT id,username,email,roles FROM users WHERE id=$1 AND delete_at=0`, id).Scan(&user.ID, &user.Username, &user.Email, &user.Roles)
	return &user, err
}

func bindPluginTestApplication(t *testing.T, db *store.DB, host *Host, events *pluginTestEventSink, logger *slog.Logger) *postcommand.Service {
	t.Helper()
	channelService := channels.New(db)
	postService := posts.New(db)
	fileService := files.New(db, files.NewFSStorage(t.TempDir()))
	auditService := audit.New(db, logger)
	service := postcommand.New(postcommand.Dependencies{
		Channels: channelService,
		Posts:    postService,
		Files:    fileService,
		Users:    pluginTestUserDirectory{db: db},
		Plugins:  host,
		Events:   events,
		Audit:    auditService,
		AuthorizeCreate: func(context.Context, string, string) (bool, error) {
			return true, nil
		},
		Logger: logger,
	})
	host.BindApplicationServices(service, fileService, events, auditService)
	return service
}

type pluginPostSnapshot struct {
	id           string
	rootID       string
	postType     string
	streamStatus string
	channelType  string
}

func waitForPluginPost(t *testing.T, db *store.DB, pluginID, messageFragment string) pluginPostSnapshot {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var snapshot pluginPostSnapshot
		err := db.Pool.QueryRow(context.Background(), `
			SELECT p.id,p.root_id,COALESCE(p.props->>'_moyro_post_type',''),
			       COALESCE(p.props->>'langflow_stream_status',''),c.type
			FROM posts p JOIN channels c ON c.id=p.channel_id
			WHERE p.props->>'plugin_id'=$1 AND p.message LIKE '%' || $2 || '%'
			ORDER BY p.create_at DESC LIMIT 1
		`, pluginID, messageFragment).Scan(&snapshot.id, &snapshot.rootID, &snapshot.postType, &snapshot.streamStatus, &snapshot.channelType)
		if err == nil {
			return snapshot
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("query %s plugin post: %v", pluginID, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s post containing %q", pluginID, messageFragment)
	return pluginPostSnapshot{}
}

func pluginRequest(t *testing.T, host *Host, pluginID, userID, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("Mattermost-User-ID", userID)
	recorder := httptest.NewRecorder()
	host.ServePluginHTTP(recorder, req, pluginID)
	return recorder
}

func waitForPluginHTTP(t *testing.T, host *Host, pluginID, userID, target, fragment string) *httptest.ResponseRecorder {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var response *httptest.ResponseRecorder
	for time.Now().Before(deadline) {
		response = pluginRequest(t, host, pluginID, userID, target)
		if response.Code == http.StatusOK && strings.Contains(response.Body.String(), fragment) {
			return response
		}
		time.Sleep(100 * time.Millisecond)
	}
	if response == nil {
		response = httptest.NewRecorder()
	}
	t.Fatalf("timed out waiting for plugin HTTP %s to contain %q: status=%d body=%s", target, fragment, response.Code, response.Body.String())
	return response
}

func assertPluginState(t *testing.T, host *Host, id, want string) {
	t.Helper()
	p, ok := host.plugin(id)
	if !ok || p.State != want {
		t.Fatalf("plugin %s = %#v, want state %s", id, p, want)
	}
}

func pluginArchivePath(t *testing.T, envName, relativeFromProject string) string {
	t.Helper()
	if configured := strings.TrimSpace(os.Getenv(envName)); configured != "" {
		if _, err := os.Stat(configured); err != nil {
			t.Fatalf("%s: %v", envName, err)
		}
		return configured
	}
	path, err := filepath.Abs(filepath.Join("..", "..", "..", "..", relativeFromProject))
	if err != nil {
		t.Fatalf("resolve %s: %v", envName, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Skipf("%s is not set and default archive is unavailable: %v", envName, err)
	}
	return path
}

func seedPluginRuntimeData(t *testing.T, ctx context.Context, db *store.DB) {
	t.Helper()
	now := time.Now().UnixMilli()
	kst := time.FixedZone("KST", 9*60*60)
	localYesterday := time.Now().In(kst).AddDate(0, 0, -1)
	yesterday := time.Date(localYesterday.Year(), localYesterday.Month(), localYesterday.Day(), 12, 0, 0, 0, kst).UnixMilli()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users (id,username,email,password_hash,roles,create_at,update_at,is_bot) VALUES ($1,$2,$3,'x','system_admin system_user',$4,$4,FALSE)`, []any{"admin-user", "admin", "admin@plugin.test", now}},
		{`INSERT INTO teams (id,name,display_name,type,create_at,update_at) VALUES ('team-1','plugin-team','Plugin Team','O',$1,$1)`, []any{now}},
		{`INSERT INTO team_members (team_id,user_id,roles,create_at) VALUES ('team-1','admin-user','team_admin team_user',$1)`, []any{now}},
		{`INSERT INTO channels (id,team_id,type,display_name,name,create_at,update_at) VALUES ('channel-1','team-1','O','Plugin Channel','plugin-channel',$1,$1)`, []any{now}},
		{`INSERT INTO channel_members (channel_id,user_id,roles,create_at) VALUES ('channel-1','admin-user','channel_admin channel_user',$1)`, []any{now}},
		{`INSERT INTO posts (id,channel_id,user_id,message,create_at,update_at) VALUES ('post-1','channel-1','admin-user','plugin compatibility message',$1,$1)`, []any{now}},
		{`INSERT INTO posts (id,channel_id,user_id,message,create_at,update_at) VALUES ('post-yesterday','channel-1','admin-user','yesterday compatibility decision',$1,$1)`, []any{yesterday}},
	}
	for _, statement := range statements {
		if _, err := db.Pool.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("seed plugin runtime: %v", err)
		}
	}
}

func newPluginRuntimeTestDB(t *testing.T) *store.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(pluginRuntimeTestPostgresDSN))
	if dsn == "" {
		t.Skipf("%s is not set", pluginRuntimeTestPostgresDSN)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open plugin test PostgreSQL: %v", err)
	}
	if err := adminPool.Ping(ctx); err != nil {
		adminPool.Close()
		t.Fatalf("ping plugin test PostgreSQL: %v", err)
	}
	schemaName := "moyro_plugins_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schemaName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		adminPool.Close()
		t.Fatalf("create plugin test schema: %v", err)
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse plugin test DSN: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = quoted
	config.MaxConns = 8
	testPool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open isolated plugin test pool: %v", err)
	}
	t.Cleanup(func() {
		testPool.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if _, err := adminPool.Exec(cleanupCtx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Errorf("drop plugin test schema: %v", err)
		}
		adminPool.Close()
	})
	return &store.DB{Pool: testPool}
}
