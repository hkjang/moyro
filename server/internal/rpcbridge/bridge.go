// Package rpcbridge talks to plugin subprocesses over a HashiCorp go-plugin
// compatible handshake. The wire format is:
//
//	Host starts plugin with MagicCookie env var set.
//	Plugin binds a local TCP listener, writes one handshake line to stdout,
//	then serves Go's stdlib net/rpc on that listener.
//
// The handshake line looks like:
//
//	1|1|tcp|127.0.0.1:54321|netrpc
//
// Subsequent stdout lines are treated as log output. Stderr is always log.
package rpcbridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/rpc"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Client is the host-side handle to a running plugin.
type Client struct {
	cmd    *exec.Cmd
	rpc    *rpc.Client
	conn   net.Conn
	stdin  io.WriteCloser
	logger *slog.Logger

	mu     sync.Mutex
	closed bool
}

// Launch starts the plugin executable and waits for the handshake.
func Launch(ctx context.Context, exe string, logger *slog.Logger) (*Client, error) {
	cmd := exec.CommandContext(ctx, exe)
	// Native plugins are operator-provisioned, fully trusted code. Keep
	// their direct command environment minimal as defense-in-depth hygiene: this
	// is not a sandbox or a secret-isolation boundary because the process shares
	// the service UID, process namespace, volume, and network. The RPC SDK itself
	// only needs the handshake cookie.
	cmd.Env = pluginEnvironment()

	// A stdin pipe is opened so closing it later is the graceful shutdown
	// signal for the plugin. The plugin SDK exits on EOF.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	// Stderr is always logs. Stream it in the background.
	go streamLogs(stderr, logger, "plugin.stderr")

	// First stdout line is the handshake; everything after is logs.
	reader := bufio.NewReader(stdout)
	handshakeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		line, err := reader.ReadString('\n')
		if err != nil {
			errCh <- err
			return
		}
		handshakeCh <- strings.TrimRight(line, "\r\n")
	}()

	var line string
	select {
	case line = <-handshakeCh:
	case err := <-errCh:
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("handshake read: %w", err)
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		return nil, errors.New("handshake timeout")
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		return nil, ctx.Err()
	}

	// Remaining stdout bytes are log output.
	go streamLogs(reader, logger, "plugin.stdout")

	addr, err := parseHandshake(line)
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}

	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("dial plugin %s: %w", addr, err)
	}

	return &Client{
		cmd:    cmd,
		rpc:    rpc.NewClient(conn),
		conn:   conn,
		stdin:  stdin,
		logger: logger,
	}, nil
}

func pluginEnvironment() []string {
	return []string{MagicCookieKey + "=" + MagicCookieValue}
}

func streamLogs(r io.Reader, logger *slog.Logger, source string) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1<<16), 4<<20)
	for scanner.Scan() {
		logger.Info(source, "line", scanner.Text())
	}
}

func parseHandshake(line string) (string, error) {
	parts := strings.Split(line, "|")
	if len(parts) < 5 {
		return "", fmt.Errorf("bad handshake %q", line)
	}
	core, app, network, addr, proto := parts[0], parts[1], parts[2], parts[3], parts[4]
	if core != fmt.Sprint(CoreProtocolVersion) {
		return "", fmt.Errorf("unsupported core protocol %s", core)
	}
	if app != fmt.Sprint(AppProtocolVersion) {
		return "", fmt.Errorf("unsupported app protocol %s", app)
	}
	if network != "tcp" {
		return "", fmt.Errorf("unsupported network %s", network)
	}
	if proto != "netrpc" {
		return "", fmt.Errorf("unsupported rpc protocol %s", proto)
	}
	return addr, nil
}

// call invokes ServiceName.method with an optional JSON payload. The plugin
// wraps everything in a Raw envelope so we don't have to keep the gob schemas
// perfectly in sync as hooks evolve.
func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("plugin closed")
	}
	c.mu.Unlock()

	var args Raw
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return err
		}
		args.Data = raw
	}
	var reply Raw
	done := make(chan *rpc.Call, 1)
	c.rpc.Go(ServiceName+"."+method, &args, &reply, done)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case call := <-done:
		if call.Error != nil {
			return fmt.Errorf("plugin %s: %w", method, call.Error)
		}
	}
	if out != nil && len(reply.Data) > 0 {
		return json.Unmarshal(reply.Data, out)
	}
	return nil
}

// ---- Hook surface ----

func (c *Client) OnActivate(ctx context.Context) error {
	return c.call(ctx, "OnActivate", nil, nil)
}

func (c *Client) OnDeactivate(ctx context.Context) error {
	return c.call(ctx, "OnDeactivate", nil, nil)
}

func (c *Client) OnConfigurationChange(ctx context.Context, cfg json.RawMessage) error {
	return c.call(ctx, "OnConfigurationChange", cfg, nil)
}

type PostEvent struct {
	Post json.RawMessage `json:"post"`
}

type Decision struct {
	Post     json.RawMessage `json:"post,omitempty"`
	Rejected bool            `json:"rejected,omitempty"`
	Reason   string          `json:"reason,omitempty"`
}

func (c *Client) MessageWillBePosted(ctx context.Context, ev PostEvent) (*Decision, error) {
	var out Decision
	if err := c.call(ctx, "MessageWillBePosted", ev, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) MessageHasBeenPosted(ctx context.Context, ev PostEvent) error {
	return c.call(ctx, "MessageHasBeenPosted", ev, nil)
}

type ServeHTTPReq struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	Body    []byte            `json:"body,omitempty"`
}

type ServeHTTPResp struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    []byte            `json:"body,omitempty"`
}

func (c *Client) ServeHTTP(ctx context.Context, req ServeHTTPReq) (*ServeHTTPResp, error) {
	var out ServeHTTPResp
	if err := c.call(ctx, "ServeHTTP", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CommandArgs matches the slashcmd.ExecuteArgs shape. Duplicated here so
// the rpcbridge package stays free of service-layer imports.
type CommandArgs struct {
	Trigger   string `json:"trigger"`
	Arg       string `json:"arg"`
	ChannelID string `json:"channel_id"`
	UserID    string `json:"user_id"`
}

// CommandReply mirrors Mattermost's command response + a Handled flag that
// lets the host fall through to the next plugin when unset.
type CommandReply struct {
	Handled      bool   `json:"handled"`
	ResponseType string `json:"response_type,omitempty"` // "in_channel" | "ephemeral"
	Text         string `json:"text,omitempty"`
}

func (c *Client) ExecuteCommand(ctx context.Context, args CommandArgs) (*CommandReply, error) {
	var out CommandReply
	if err := c.call(ctx, "ExecuteCommand", args, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()

	if c.rpc != nil {
		_ = c.rpc.Close()
	}
	if c.conn != nil {
		_ = c.conn.Close()
	}
	if c.stdin != nil {
		_ = c.stdin.Close() // graceful — plugin exits on EOF
	}
	if c.cmd != nil && c.cmd.Process != nil {
		return c.cmd.Process.Kill()
	}
	return nil
}
