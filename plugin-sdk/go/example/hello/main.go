// Example moyro server plugin: echoes OnActivate and appends "!" to posts.
package main

import (
	"context"
	"encoding/json"

	"github.com/hkjang/moyro/plugin-sdk/go/plugin"
)

type Hello struct{}

func (h *Hello) OnActivate(ctx context.Context) error   { return nil }
func (h *Hello) OnDeactivate(ctx context.Context) error { return nil }

func (h *Hello) MessageWillBePosted(ctx context.Context, post json.RawMessage) (json.RawMessage, bool, string, error) {
	var p map[string]any
	if err := json.Unmarshal(post, &p); err != nil {
		return nil, false, "", err
	}
	if msg, ok := p["message"].(string); ok {
		p["message"] = msg + " !"
	}
	mod, _ := json.Marshal(p)
	return mod, false, "", nil
}

func (h *Hello) MessageHasBeenPosted(ctx context.Context, post json.RawMessage) error {
	return nil
}

func main() {
	plugin.Serve(&Hello{})
}
