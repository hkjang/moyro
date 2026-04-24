package pluginhost

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"testing"

	"github.com/moddle/moddle/server/internal/rpcbridge"
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
