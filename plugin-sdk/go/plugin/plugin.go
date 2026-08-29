// Package plugin is the server-side moyro Plugin SDK. Plugin authors embed
// this and implement the Hooks they care about; the package performs the
// HashiCorp go-plugin compatible handshake with the host and serves net/rpc.
package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/rpc"
	"os"
)

// Protocol constants — kept in sync with server/internal/rpcbridge/protocol.go
// so plugin binaries stay a single import away from the host contract.
const (
	coreProtocolVersion = 1
	appProtocolVersion  = 1

	magicCookieKey   = "MOYRO_PLUGIN"
	magicCookieValue = "moyro.v1"

	serviceName = "Moyro"
)

// Hooks is the minimum contract for every plugin. Implement additional
// optional interfaces (ConfigHook, MessageHook, HTTPHook) to extend.
type Hooks interface {
	OnActivate(ctx context.Context) error
	OnDeactivate(ctx context.Context) error
}

type ConfigHook interface {
	OnConfigurationChange(ctx context.Context, raw json.RawMessage) error
}

type MessageHook interface {
	// Return (modified, rejected, reason, err). If modified is nil and
	// rejected is false, the post passes through unchanged.
	MessageWillBePosted(ctx context.Context, post json.RawMessage) (modified json.RawMessage, rejected bool, reason string, err error)
	MessageHasBeenPosted(ctx context.Context, post json.RawMessage) error
}

type HTTPHook interface {
	ServeHTTP(ctx context.Context, req HTTPRequest) (HTTPResponse, error)
}

type HTTPRequest struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	Body    []byte            `json:"body,omitempty"`
}

type HTTPResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    []byte            `json:"body,omitempty"`
}

// Raw is the gob envelope shared with the host — JSON-in-bytes.
type Raw struct {
	Data []byte
}

// Serve performs the handshake with the host and blocks serving RPC calls.
// Call it from your plugin's main().
func Serve(hooks Hooks) {
	if os.Getenv(magicCookieKey) != magicCookieValue {
		fmt.Fprintln(os.Stderr, "This is a moyro plugin and cannot be executed directly.")
		os.Exit(1)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatal("listen: %v", err)
	}
	defer listener.Close()

	srv := rpc.NewServer()
	if err := srv.RegisterName(serviceName, &rpcAdapter{hooks: hooks}); err != nil {
		fatal("register: %v", err)
	}

	addr := listener.Addr().(*net.TCPAddr)
	fmt.Printf("%d|%d|tcp|%s|netrpc\n", coreProtocolVersion, appProtocolVersion, addr.String())

	// Close stdin closure is the host's signal for shutdown.
	go watchStdin()

	srv.Accept(listener)
}

func watchStdin() {
	buf := make([]byte, 256)
	for {
		_, err := os.Stdin.Read(buf)
		if err != nil {
			os.Exit(0)
		}
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

// rpcAdapter binds the user-supplied Hooks to net/rpc method signatures.
// Every method takes a Raw envelope so hook schemas can evolve without
// breaking gob.
type rpcAdapter struct {
	hooks Hooks
}

func (a *rpcAdapter) OnActivate(args *Raw, reply *Raw) error {
	return a.hooks.OnActivate(context.Background())
}

func (a *rpcAdapter) OnDeactivate(args *Raw, reply *Raw) error {
	return a.hooks.OnDeactivate(context.Background())
}

func (a *rpcAdapter) OnConfigurationChange(args *Raw, reply *Raw) error {
	h, ok := a.hooks.(ConfigHook)
	if !ok {
		return nil
	}
	return h.OnConfigurationChange(context.Background(), args.Data)
}

func (a *rpcAdapter) MessageWillBePosted(args *Raw, reply *Raw) error {
	h, ok := a.hooks.(MessageHook)
	if !ok {
		return nil
	}
	var ev struct {
		Post json.RawMessage `json:"post"`
	}
	if len(args.Data) > 0 {
		if err := json.Unmarshal(args.Data, &ev); err != nil {
			return err
		}
	}
	mod, rej, reason, err := h.MessageWillBePosted(context.Background(), ev.Post)
	if err != nil {
		return err
	}
	out := struct {
		Post     json.RawMessage `json:"post,omitempty"`
		Rejected bool            `json:"rejected,omitempty"`
		Reason   string          `json:"reason,omitempty"`
	}{Post: mod, Rejected: rej, Reason: reason}
	raw, err := json.Marshal(out)
	if err != nil {
		return err
	}
	reply.Data = raw
	return nil
}

func (a *rpcAdapter) MessageHasBeenPosted(args *Raw, reply *Raw) error {
	h, ok := a.hooks.(MessageHook)
	if !ok {
		return nil
	}
	var ev struct {
		Post json.RawMessage `json:"post"`
	}
	if len(args.Data) > 0 {
		if err := json.Unmarshal(args.Data, &ev); err != nil {
			return err
		}
	}
	return h.MessageHasBeenPosted(context.Background(), ev.Post)
}

func (a *rpcAdapter) ServeHTTP(args *Raw, reply *Raw) error {
	h, ok := a.hooks.(HTTPHook)
	if !ok {
		reply.Data = []byte(`{"status":404}`)
		return nil
	}
	var req HTTPRequest
	if len(args.Data) > 0 {
		if err := json.Unmarshal(args.Data, &req); err != nil {
			return err
		}
	}
	resp, err := h.ServeHTTP(context.Background(), req)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	reply.Data = raw
	return nil
}

// Sentinel to keep errors helpful for plugin authors.
var ErrNotImplemented = errors.New("hook not implemented")
