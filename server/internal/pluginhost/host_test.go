package pluginhost

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hkjang/moyro/server/internal/activityevents"
	"github.com/hkjang/moyro/server/internal/rpcbridge"
	"github.com/hkjang/moyro/server/internal/ws"
	mmmodel "github.com/mattermost/mattermost/server/public/model"
	mmplugin "github.com/mattermost/mattermost/server/public/plugin"
)

type fakePluginClient struct {
	id          string
	calls       *[]string
	willPosts   *[][]byte
	will        func(context.Context, rpcbridge.PostEvent) (*rpcbridge.Decision, error)
	has         func(context.Context, rpcbridge.PostEvent) error
	command     func(context.Context, rpcbridge.CommandArgs) (*rpcbridge.CommandReply, error)
	deactivate  func(context.Context) error
	closeClient func() error
}

type pluginActivityRecorder struct{ inputs []activityevents.EmitInput }

func (r *pluginActivityRecorder) Emit(_ context.Context, input activityevents.EmitInput) (*activityevents.Event, error) {
	r.inputs = append(r.inputs, input)
	return &activityevents.Event{ID: "activity-1", Type: input.Type, Title: input.Title}, nil
}

type pluginWebSocketRecorder struct{ events []ws.Event }

func (r *pluginWebSocketRecorder) Broadcast(event ws.Event) { r.events = append(r.events, event) }

func (f *fakePluginClient) record(name string) {
	if f.calls != nil {
		*f.calls = append(*f.calls, f.id+"."+name)
	}
}

func (f *fakePluginClient) OnActivate(context.Context) error {
	f.record("activate")
	return nil
}

func (f *fakePluginClient) OnDeactivate(ctx context.Context) error {
	f.record("deactivate")
	if f.deactivate != nil {
		return f.deactivate(ctx)
	}
	return nil
}

func (f *fakePluginClient) Close() error {
	f.record("close")
	if f.closeClient != nil {
		return f.closeClient()
	}
	return nil
}

func (f *fakePluginClient) MessageWillBePosted(ctx context.Context, ev rpcbridge.PostEvent) (*rpcbridge.Decision, error) {
	f.record("will")
	if f.willPosts != nil {
		*f.willPosts = append(*f.willPosts, append([]byte(nil), ev.Post...))
	}
	if f.will != nil {
		return f.will(ctx, ev)
	}
	return &rpcbridge.Decision{}, nil
}

func (f *fakePluginClient) MessageHasBeenPosted(ctx context.Context, ev rpcbridge.PostEvent) error {
	f.record("has")
	if f.has != nil {
		return f.has(ctx, ev)
	}
	return nil
}

func (f *fakePluginClient) ExecuteCommand(ctx context.Context, args rpcbridge.CommandArgs) (*rpcbridge.CommandReply, error) {
	f.record("command")
	if f.command != nil {
		return f.command(ctx, args)
	}
	return &rpcbridge.CommandReply{}, nil
}

func newTestHost() *Host {
	return New("", slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func registerTestPlugin(h *Host, id, version, state string, client pluginClient) {
	h.register(&Plugin{
		Manifest: &Manifest{ID: id, Version: version},
		Dir:      id,
		State:    state,
		Enabled:  true,
		client:   client,
	})
}

func TestListUsesRegistrationOrderAndReplacesDuplicates(t *testing.T) {
	h := newTestHost()
	registerTestPlugin(h, "beta", "1.0.0", "installed", nil)
	registerTestPlugin(h, "alpha", "1.0.0", "installed", nil)
	registerTestPlugin(h, "beta", "2.0.0", "running", nil)

	list := h.List()
	if len(list) != 2 {
		t.Fatalf("List() length = %d, want 2", len(list))
	}
	if list[0]["id"] != "beta" || list[1]["id"] != "alpha" {
		t.Fatalf("List() order = %#v, want beta then alpha", list)
	}
	if list[0]["version"] != "2.0.0" || list[0]["state"] != "running" {
		t.Fatalf("List()[0] = %#v, want updated beta plugin", list[0])
	}
}

func TestMessageWillBePostedRunsRunningPluginsInOrder(t *testing.T) {
	h := newTestHost()
	var calls []string
	var seen [][]byte
	registerTestPlugin(h, "first", "1.0.0", "running", &fakePluginClient{
		id:        "first",
		calls:     &calls,
		willPosts: &seen,
		will: func(context.Context, rpcbridge.PostEvent) (*rpcbridge.Decision, error) {
			return &rpcbridge.Decision{Post: []byte(`{"stage":1}`)}, nil
		},
	})
	registerTestPlugin(h, "installed", "1.0.0", "installed", &fakePluginClient{id: "installed", calls: &calls})
	registerTestPlugin(h, "second", "1.0.0", "running", &fakePluginClient{
		id:        "second",
		calls:     &calls,
		willPosts: &seen,
		will: func(context.Context, rpcbridge.PostEvent) (*rpcbridge.Decision, error) {
			return &rpcbridge.Decision{Post: []byte(`{"stage":2}`)}, nil
		},
	})

	got, rejected, reason := h.MessageWillBePosted(context.Background(), []byte(`{"stage":0}`))
	if rejected {
		t.Fatalf("rejected = true, reason = %q", reason)
	}
	if string(got) != `{"stage":2}` {
		t.Fatalf("post = %s, want final transformed post", got)
	}
	if want := []string{"first.will", "second.will"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
	if gotSeen := []string{string(seen[0]), string(seen[1])}; !reflect.DeepEqual(gotSeen, []string{`{"stage":0}`, `{"stage":1}`}) {
		t.Fatalf("seen posts = %#v, want original then transformed", gotSeen)
	}
}

func TestMessageWillBePostedStopsOnRejection(t *testing.T) {
	h := newTestHost()
	var calls []string
	registerTestPlugin(h, "first", "1.0.0", "running", &fakePluginClient{
		id:    "first",
		calls: &calls,
		will: func(context.Context, rpcbridge.PostEvent) (*rpcbridge.Decision, error) {
			return &rpcbridge.Decision{Post: []byte(`{"ok":true}`)}, nil
		},
	})
	registerTestPlugin(h, "rejector", "1.0.0", "running", &fakePluginClient{
		id:    "rejector",
		calls: &calls,
		will: func(context.Context, rpcbridge.PostEvent) (*rpcbridge.Decision, error) {
			return &rpcbridge.Decision{Rejected: true, Reason: "blocked"}, nil
		},
	})
	registerTestPlugin(h, "last", "1.0.0", "running", &fakePluginClient{id: "last", calls: &calls})

	got, rejected, reason := h.MessageWillBePosted(context.Background(), []byte(`{"ok":false}`))
	if got != nil {
		t.Fatalf("post = %s, want nil after rejection", got)
	}
	if !rejected || reason != "blocked" {
		t.Fatalf("rejected = %v, reason = %q; want true, blocked", rejected, reason)
	}
	if want := []string{"first.will", "rejector.will"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestExecuteCommandSkipsErrorsAndStopsAtFirstHandledReply(t *testing.T) {
	h := newTestHost()
	var calls []string
	registerTestPlugin(h, "erroring", "1.0.0", "running", &fakePluginClient{
		id:    "erroring",
		calls: &calls,
		command: func(context.Context, rpcbridge.CommandArgs) (*rpcbridge.CommandReply, error) {
			return nil, errors.New("temporary plugin error")
		},
	})
	registerTestPlugin(h, "unhandled", "1.0.0", "running", &fakePluginClient{id: "unhandled", calls: &calls})
	registerTestPlugin(h, "handler", "1.0.0", "running", &fakePluginClient{
		id:    "handler",
		calls: &calls,
		command: func(context.Context, rpcbridge.CommandArgs) (*rpcbridge.CommandReply, error) {
			return &rpcbridge.CommandReply{Handled: true, Text: "done"}, nil
		},
	})
	registerTestPlugin(h, "late", "1.0.0", "running", &fakePluginClient{id: "late", calls: &calls})

	reply, handled, err := h.ExecuteCommand(context.Background(), "/ship", "it", "c1", "u1")
	if err != nil {
		t.Fatalf("ExecuteCommand() returned error: %v", err)
	}
	if !handled || reply == nil || reply.Text != "done" {
		t.Fatalf("reply = %#v, handled = %v; want handled reply", reply, handled)
	}
	if want := []string{"erroring.command", "unhandled.command", "handler.command"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestDisableWaitsForInFlightDispatch(t *testing.T) {
	h := newTestHost()
	hookStarted := make(chan struct{})
	releaseHook := make(chan struct{})
	deactivated := make(chan struct{}, 1)
	registerTestPlugin(h, "blocking", "1.0.0", "running", &fakePluginClient{
		id: "blocking",
		will: func(context.Context, rpcbridge.PostEvent) (*rpcbridge.Decision, error) {
			close(hookStarted)
			<-releaseHook
			return &rpcbridge.Decision{}, nil
		},
		deactivate: func(context.Context) error {
			deactivated <- struct{}{}
			return nil
		},
	})
	p, ok := h.plugin("blocking")
	if !ok {
		t.Fatal("blocking plugin was not registered")
	}

	postDone := make(chan struct{})
	go func() {
		defer close(postDone)
		h.MessageWillBePosted(context.Background(), []byte(`{"message":"in flight"}`))
	}()
	select {
	case <-hookStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("plugin hook did not start")
	}

	disableCtx, cancelDisable := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelDisable()
	disableDone := make(chan error, 1)
	go func() {
		_, err := h.Disable(disableCtx, "blocking")
		disableDone <- err
	}()

	deadline := time.NewTimer(5 * time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for !p.transitioning.Load() {
		select {
		case err := <-disableDone:
			t.Fatalf("Disable returned before closing admission: %v", err)
		case <-deadline.C:
			t.Fatal("Disable did not close dispatch admission")
		case <-ticker.C:
		}
	}
	select {
	case <-deactivated:
		t.Fatal("plugin was deactivated while its dispatch lease was in flight")
	default:
	}
	select {
	case err := <-disableDone:
		t.Fatalf("Disable returned while its dispatch lease was in flight: %v", err)
	default:
	}

	close(releaseHook)
	select {
	case <-postDone:
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight hook did not finish")
	}
	select {
	case err := <-disableDone:
		if err != nil {
			t.Fatalf("Disable returned error after dispatch drained: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Disable did not resume after dispatch drained")
	}
	select {
	case <-deactivated:
	default:
		t.Fatal("Disable did not deactivate the plugin after dispatch drained")
	}
	if _, acquired := p.acquireDispatch(); acquired {
		t.Fatal("disabled plugin admitted a new dispatch")
	}
}

type generationHooks struct {
	mmplugin.Hooks
	implemented []string
}

func (h *generationHooks) Implemented() ([]string, error) {
	return append([]string(nil), h.implemented...), nil
}

func TestMattermostDispatchLeaseBindsHookGeneration(t *testing.T) {
	h := newTestHost()
	const pluginID = "same-id"
	oldHooks := &generationHooks{implemented: []string{"MessageHasBeenPosted"}}
	old := &Plugin{
		Manifest: &Manifest{ID: pluginID, Version: "1.0.0"},
		Dir:      pluginID, State: "running", Runtime: RuntimeMattermostV1,
		Enabled: true, mmHooks: oldHooks, mmImplemented: map[string]struct{}{"MessageHasBeenPosted": {}},
	}
	h.register(old)
	lease, ok := old.acquireDispatch()
	if !ok {
		t.Fatal("old plugin generation did not admit a dispatch")
	}
	defer lease.release()

	newHooks := &generationHooks{implemented: []string{"MessageHasBeenPosted"}}
	h.register(&Plugin{
		Manifest: &Manifest{ID: pluginID, Version: "2.0.0"},
		Dir:      pluginID, State: "running", Runtime: RuntimeMattermostV1,
		Enabled: true, mmHooks: newHooks, mmImplemented: map[string]struct{}{"MessageHasBeenPosted": {}},
	})

	hooks, implemented := h.mattermostHooks(lease, "MessageHasBeenPosted")
	if !implemented {
		t.Fatal("old generation hook was not reported as implemented")
	}
	if hooks != oldHooks {
		t.Fatalf("dispatch lease resolved hooks from a different generation: got %p, want %p", hooks, oldHooks)
	}
	if hooks == newHooks {
		t.Fatal("old dispatch lease resolved the same-ID replacement hook")
	}
}

func TestShutdownDeactivatesAndClosesInOrder(t *testing.T) {
	h := newTestHost()
	var calls []string
	registerTestPlugin(h, "first", "1.0.0", "running", &fakePluginClient{id: "first", calls: &calls})
	registerTestPlugin(h, "second", "1.0.0", "running", &fakePluginClient{id: "second", calls: &calls})

	h.Shutdown()

	want := []string{"first.deactivate", "first.close", "second.deactivate", "second.close"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
	if running := h.runningPlugins(); len(running) != 0 {
		t.Fatalf("runningPlugins() length = %d, want 0", len(running))
	}
	list := h.List()
	for _, item := range list {
		if item["state"] != "installed" {
			t.Fatalf("state after shutdown = %#v, want installed", list)
		}
	}
}

func TestMattermostAPIAdmissionLifecycle(t *testing.T) {
	host := New(t.TempDir(), slog.Default())
	plugin := &Plugin{
		Manifest: &Manifest{ID: "com.example.lifecycle", Version: "1.0.0"},
		Runtime:  RuntimeMattermostV1,
		State:    "running",
		Enabled:  true,
	}
	host.register(plugin)
	api := &mattermostAPI{host: host, pluginID: plugin.Manifest.ID, generation: plugin, logger: slog.Default()}

	if got := api.GetServerVersion(); got == "" {
		t.Fatal("activation/running generation API was rejected")
	}
	if err := plugin.closeDispatchAdmission(context.Background()); err != nil {
		t.Fatalf("close admission: %v", err)
	}
	if got := api.GetServerVersion(); got != "" {
		t.Fatalf("stale generation API remained admitted after close: %q", got)
	}

	plugin.apiDeactivating.Store(true)
	if got := api.GetServerVersion(); got == "" {
		t.Fatal("synchronous OnDeactivate cleanup API was rejected")
	}
	plugin.apiDeactivating.Store(false)
	if got := api.GetServerVersion(); got != "" {
		t.Fatalf("old generation API remained admitted after OnDeactivate: %q", got)
	}
	plugin.apiActivating.Store(true)
	if got := api.GetServerVersion(); got == "" {
		t.Fatal("re-enable OnActivate API was rejected")
	}
	plugin.apiActivating.Store(false)
	if got := api.GetServerVersion(); got != "" {
		t.Fatalf("closed generation API remained admitted after OnActivate: %q", got)
	}

	plugin.openDispatchAdmission()
	if got := api.GetServerVersion(); got == "" {
		t.Fatal("rollback/reactivation did not reopen API admission")
	}
}

func TestMattermostUserScopedWebSocketEventAlsoCreatesSafeDurableActivity(t *testing.T) {
	host := New(t.TempDir(), slog.Default())
	plugin := &Plugin{
		Manifest: &Manifest{ID: "com.example.alerts", Version: "1.0.0"},
		Runtime:  RuntimeMattermostV1, State: "running", Enabled: true,
	}
	host.register(plugin)
	websocketEvents := &pluginWebSocketRecorder{}
	activity := &pluginActivityRecorder{}
	host.events = websocketEvents
	host.BindActivityEmitter(activity)
	api := &mattermostAPI{host: host, pluginID: plugin.Manifest.ID, generation: plugin, logger: slog.Default()}

	api.PublishWebSocketEvent("job_finished", map[string]any{"secret": "must-not-be-persisted"}, &mmmodel.WebsocketBroadcast{UserId: "user-1"})

	if len(websocketEvents.events) != 1 || websocketEvents.events[0].Broadcast.UserID != "user-1" {
		t.Fatalf("websocket events = %#v", websocketEvents.events)
	}
	if len(activity.inputs) != 1 {
		t.Fatalf("activity inputs = %#v", activity.inputs)
	}
	input := activity.inputs[0]
	if input.UserID != "user-1" || input.Type != activityevents.TypePluginEvent || input.ResourceID != plugin.Manifest.ID {
		t.Fatalf("activity input = %#v", input)
	}
	if input.Summary != "" || strings.Contains(input.Title, "must-not-be-persisted") {
		t.Fatalf("plugin payload leaked into durable activity: %#v", input)
	}
}

func TestPluginActivityTitleIsBoundedAndRemovesControls(t *testing.T) {
	t.Parallel()
	title := pluginActivityTitle("  job\x00finished  " + strings.Repeat("가", 400))
	if strings.ContainsRune(title, '\x00') || len([]rune(title)) != 256 || !strings.HasSuffix(title, "…") {
		t.Fatalf("plugin activity title = %q (%d runes)", title, len([]rune(title)))
	}
	if got := pluginActivityTitle("\x00\n\t"); got != "플러그인 알림: 업데이트" {
		t.Fatalf("empty plugin activity title = %q", got)
	}
}
