package webhooks

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/hkjang/moyro/server/internal/application/postcommand"
	"github.com/hkjang/moyro/server/internal/posts"
)

type incomingCommandStub struct {
	commands []postcommand.Command
	post     *posts.Post
	err      error
}

func (s *incomingCommandStub) Execute(_ context.Context, command postcommand.Command) (*posts.Post, error) {
	s.commands = append(s.commands, command)
	return s.post, s.err
}

func TestIncomingFireUsesPostCommandAndPayloadOverrides(t *testing.T) {
	wantPost := &posts.Post{ID: "post-1"}
	executor := &incomingCommandStub{post: wantPost}
	service := NewIncoming(nil, executor)
	hook := &IncomingHook{
		ID: "hook-1", CreatorID: "creator-1", ChannelID: "channel-1",
		Username: "configured-name", IconURL: "https://configured.test/icon.png",
	}

	got, err := service.Fire(context.Background(), hook, IncomingPayload{
		Text: "build complete", Username: "payload-name", IconURL: "https://payload.test/icon.png",
		WebhookDepth: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != wantPost {
		t.Fatalf("post = %#v, want %#v", got, wantPost)
	}
	wantCommand := postcommand.Command{
		Source:           postcommand.SourceIncomingWebhook,
		ActorID:          "creator-1",
		ChannelID:        "channel-1",
		Message:          "build complete",
		CredentialID:     "hook-1",
		OverrideUsername: "payload-name",
		OverrideIconURL:  "https://payload.test/icon.png",
		SenderName:       "configured-name",
		WebhookDepth:     2,
	}
	if !reflect.DeepEqual(executor.commands, []postcommand.Command{wantCommand}) {
		t.Fatalf("commands = %#v, want %#v", executor.commands, []postcommand.Command{wantCommand})
	}
}

func TestIncomingFireBoundsPropagatedWebhookDepth(t *testing.T) {
	executor := &incomingCommandStub{post: &posts.Post{ID: "post-1"}}
	service := NewIncoming(nil, executor)

	if _, err := service.Fire(context.Background(), &IncomingHook{}, IncomingPayload{
		Text: "loop guard", WebhookDepth: maximumWebhookDepth + 100,
	}); err != nil {
		t.Fatal(err)
	}
	if got := executor.commands[0].WebhookDepth; got != maximumWebhookDepth {
		t.Fatalf("webhook depth = %d, want %d", got, maximumWebhookDepth)
	}
}

func TestIncomingFireFallsBackToConfiguredPresentation(t *testing.T) {
	executor := &incomingCommandStub{post: &posts.Post{ID: "post-1"}}
	service := NewIncoming(nil, executor)
	hook := &IncomingHook{
		CreatorID: "creator-1", ChannelID: "channel-1",
		Username: "configured-name", IconURL: "https://configured.test/icon.png",
	}

	_, err := service.Fire(context.Background(), hook, IncomingPayload{Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if len(executor.commands) != 1 || executor.commands[0].OverrideUsername != hook.Username ||
		executor.commands[0].OverrideIconURL != hook.IconURL {
		t.Fatalf("command = %#v", executor.commands)
	}
}

func TestIncomingFireRejectsEmptyTextBeforePostCommand(t *testing.T) {
	executor := &incomingCommandStub{}
	service := NewIncoming(nil, executor)

	_, err := service.Fire(context.Background(), &IncomingHook{}, IncomingPayload{Text: " \t\n"})
	if err == nil || err.Error() != "empty text" {
		t.Fatalf("error = %v", err)
	}
	if len(executor.commands) != 0 {
		t.Fatalf("commands = %#v", executor.commands)
	}
}

func TestIncomingFirePropagatesPostCommandError(t *testing.T) {
	wantErr := errors.New("post command failed")
	service := NewIncoming(nil, &incomingCommandStub{err: wantErr})

	_, err := service.Fire(context.Background(), &IncomingHook{}, IncomingPayload{Text: "hello"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}
