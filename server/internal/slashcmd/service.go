// Package slashcmd routes built-in slash commands and defers unknown commands
// to plugins via the plugin host.
//
// A command is a line beginning with "/" in the composer, e.g. "/away" or
// "/shrug hello". The first token (minus the slash) is the trigger, the rest
// is the argument string. Responses are structured as Mattermost-compatible
// ephemeral / in-channel JSON so the client renderer can treat them uniformly.
package slashcmd

import (
	"context"
	"errors"
	"strings"

	"github.com/moddle/moddle/server/internal/channels"
	"github.com/moddle/moddle/server/internal/posts"
	"github.com/moddle/moddle/server/internal/userstatus"
)

// ResponseType controls client rendering.
//
//   - "in_channel": the response becomes a real post visible to all members.
//   - "ephemeral":  only the caller sees it, never persisted.
//
// Matches Mattermost's /commands/execute contract.
type ResponseType string

const (
	InChannel ResponseType = "in_channel"
	Ephemeral ResponseType = "ephemeral"
)

// Response is what the handler returns; the caller serialises it to JSON.
type Response struct {
	ResponseType ResponseType `json:"response_type"`
	Text         string       `json:"text"`
	// Post is optional — populated when the command synthesised a real post
	// (e.g. /shrug) so the client can echo it immediately without waiting
	// for the WS `posted` round-trip.
	Post *posts.Post `json:"post,omitempty"`
}

// ExecuteArgs is the input. TeamID/ChannelID/UserID identify context.
type ExecuteArgs struct {
	TeamID    string
	ChannelID string
	UserID    string
	Command   string // full raw command string including the leading slash
}

// Plugin dispatcher interface — kept narrow so the plugin host can satisfy
// it without slashcmd depending on pluginhost directly (avoids import cycle).
type Plugin interface {
	ExecuteCommand(ctx context.Context, trigger, args string, channelID, userID string) (*Response, bool, error)
}

type Service struct {
	posts    *posts.Service
	channels *channels.Service
	status   *userstatus.Service
	plugins  Plugin // may be nil; unknown commands fail if so
}

func New(postSvc *posts.Service, chanSvc *channels.Service, statusSvc *userstatus.Service, plugin Plugin) *Service {
	return &Service{posts: postSvc, channels: chanSvc, status: statusSvc, plugins: plugin}
}

// ErrUnknown is returned when no built-in matches and no plugin handles the
// trigger. Callers should surface a 404/400 to the client.
var ErrUnknown = errors.New("unknown slash command")

// Execute routes a command. Returns (response, nil) on success, (nil, err)
// on failure. Built-ins take priority over plugins so users can't shadow
// /away or /shrug with a malicious plugin.
func (s *Service) Execute(ctx context.Context, a ExecuteArgs) (*Response, error) {
	trigger, rest := parse(a.Command)
	if trigger == "" {
		return nil, errors.New("empty command")
	}
	if fn, ok := builtins[trigger]; ok {
		return fn(ctx, s, a, rest)
	}
	if s.plugins != nil {
		resp, handled, err := s.plugins.ExecuteCommand(ctx, trigger, rest, a.ChannelID, a.UserID)
		if err != nil {
			return nil, err
		}
		if handled {
			return resp, nil
		}
	}
	return nil, ErrUnknown
}

// parse splits "/shrug hello world" into ("shrug", "hello world").
// A blank arg string is fine; parse never panics.
func parse(raw string) (string, string) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "/")
	if s == "" {
		return "", ""
	}
	if i := strings.IndexAny(s, " \t"); i > 0 {
		return strings.ToLower(s[:i]), strings.TrimSpace(s[i:])
	}
	return strings.ToLower(s), ""
}

type handler func(ctx context.Context, s *Service, a ExecuteArgs, arg string) (*Response, error)

// ---- Built-ins ----

var builtins = map[string]handler{
	"shrug":   cmdShrug,
	"me":      cmdMe,
	"code":    cmdCode,
	"away":    cmdStatus(userstatus.Away),
	"online":  cmdStatus(userstatus.Online),
	"dnd":     cmdStatus(userstatus.DND),
	"offline": cmdStatus(userstatus.Offline),
	"status":  cmdWhoAmI,
	"help":    cmdHelp,
}

// /shrug [message] — posts "<message> ¯\_(ツ)_/¯" as a normal in-channel post.
func cmdShrug(ctx context.Context, s *Service, a ExecuteArgs, arg string) (*Response, error) {
	msg := strings.TrimSpace(arg + " ¯\\_(ツ)_/¯")
	p, err := s.posts.Create(ctx, a.ChannelID, a.UserID, "", msg, nil, nil)
	if err != nil {
		return nil, err
	}
	return &Response{ResponseType: InChannel, Text: msg, Post: p}, nil
}

// /me <action> — posts in italics so other users see an action line.
// We wrap in single-asterisks for Markdown italics; the webapp treats the
// prefix as a styling hint via post.props.
func cmdMe(ctx context.Context, s *Service, a ExecuteArgs, arg string) (*Response, error) {
	if arg == "" {
		return &Response{ResponseType: Ephemeral, Text: "Usage: /me <action>"}, nil
	}
	msg := "*" + arg + "*"
	props := map[string]any{"from_me_command": true}
	p, err := s.posts.Create(ctx, a.ChannelID, a.UserID, "", msg, props, nil)
	if err != nil {
		return nil, err
	}
	return &Response{ResponseType: InChannel, Text: msg, Post: p}, nil
}

// /code <snippet> — wraps the arg in a fenced block. Everything after the
// trigger, including leading spaces, is preserved via arg.
func cmdCode(ctx context.Context, s *Service, a ExecuteArgs, arg string) (*Response, error) {
	if arg == "" {
		return &Response{ResponseType: Ephemeral, Text: "Usage: /code <snippet>"}, nil
	}
	msg := "```\n" + arg + "\n```"
	p, err := s.posts.Create(ctx, a.ChannelID, a.UserID, "", msg, nil, nil)
	if err != nil {
		return nil, err
	}
	return &Response{ResponseType: InChannel, Text: msg, Post: p}, nil
}

// cmdStatus returns a handler that flips the caller's presence. Since the
// value is captured by closure we use a factory so the map is static.
func cmdStatus(to string) handler {
	return func(ctx context.Context, s *Service, a ExecuteArgs, _ string) (*Response, error) {
		if _, err := s.status.Set(ctx, a.UserID, to, true); err != nil {
			return nil, err
		}
		return &Response{
			ResponseType: Ephemeral,
			Text:         "Status set to " + to + ".",
		}, nil
	}
}

// /status — shows the caller their own status.
func cmdWhoAmI(ctx context.Context, s *Service, a ExecuteArgs, _ string) (*Response, error) {
	st, err := s.status.Get(ctx, a.UserID)
	if err != nil {
		return nil, err
	}
	return &Response{ResponseType: Ephemeral, Text: "Your status: " + st.Status}, nil
}

// /help — lists available built-ins.
func cmdHelp(_ context.Context, _ *Service, _ ExecuteArgs, _ string) (*Response, error) {
	lines := []string{
		"Available commands:",
		"  /shrug [message]     Append ¯\\_(ツ)_/¯",
		"  /me <action>         Post an action in italics",
		"  /code <snippet>      Post a fenced code block",
		"  /away /online /dnd /offline   Change your status",
		"  /status              Show your current status",
		"  /help                Show this message",
	}
	return &Response{ResponseType: Ephemeral, Text: strings.Join(lines, "\n")}, nil
}
