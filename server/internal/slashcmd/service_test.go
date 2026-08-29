package slashcmd

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/hkjang/moyro/server/internal/application/postcommand"
	"github.com/hkjang/moyro/server/internal/posts"
)

type slashCommandStub struct {
	commands []postcommand.Command
	err      error
}

type slashPluginStub struct {
	response *Response
	handled  bool
	err      error
}

func (s *slashPluginStub) ExecuteCommand(context.Context, string, string, string, string) (*Response, bool, error) {
	return s.response, s.handled, s.err
}

func (s *slashCommandStub) Execute(_ context.Context, command postcommand.Command) (*posts.Post, error) {
	s.commands = append(s.commands, command)
	if s.err != nil {
		return nil, s.err
	}
	return &posts.Post{
		ID: "post-1", ChannelID: command.ChannelID, UserID: command.ActorID,
		Message: command.Message,
	}, nil
}

func TestBuiltInPostCommandsUseCommonPipeline(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		trigger string
		message string
	}{
		{name: "shrug", raw: "/shrug hello", trigger: "shrug", message: "hello ¯\\_(ツ)_/¯"},
		{name: "me", raw: "/me waves", trigger: "me", message: "*waves*"},
		{name: "code", raw: "/code answer := 42", trigger: "code", message: "```\nanswer := 42\n```"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor := &slashCommandStub{}
			service := New(executor, nil, nil, nil)
			args := ExecuteArgs{TeamID: "team-1", ChannelID: "channel-1", UserID: "user-1", Command: test.raw}

			response, err := service.Execute(context.Background(), args)
			if err != nil {
				t.Fatal(err)
			}
			if response.ResponseType != InChannel || response.Text != test.message ||
				response.Post == nil || response.Post.Message != test.message {
				t.Fatalf("response = %#v", response)
			}
			want := postcommand.Command{
				Source:       postcommand.SourceSlashCommand,
				ActorID:      args.UserID,
				ChannelID:    args.ChannelID,
				Message:      test.message,
				SlashCommand: test.trigger,
			}
			if !reflect.DeepEqual(executor.commands, []postcommand.Command{want}) {
				t.Fatalf("commands = %#v, want %#v", executor.commands, []postcommand.Command{want})
			}
		})
	}
}

func TestBuiltInUsageResponseDoesNotCreatePost(t *testing.T) {
	executor := &slashCommandStub{}
	service := New(executor, nil, nil, nil)

	response, err := service.Execute(context.Background(), ExecuteArgs{Command: "/me"})
	if err != nil {
		t.Fatal(err)
	}
	if response.ResponseType != Ephemeral || response.Text != "Usage: /me <action>" || response.Post != nil {
		t.Fatalf("response = %#v", response)
	}
	if len(executor.commands) != 0 {
		t.Fatalf("commands = %#v", executor.commands)
	}
}

func TestBuiltInPostCommandErrorIsPreserved(t *testing.T) {
	wantErr := errors.New("create failed")
	service := New(&slashCommandStub{err: wantErr}, nil, nil, nil)

	_, err := service.Execute(context.Background(), ExecuteArgs{
		ChannelID: "channel-1", UserID: "user-1", Command: "/code snippet",
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestPluginInChannelResponseUsesCommonPipeline(t *testing.T) {
	executor := &slashCommandStub{}
	plugin := &slashPluginStub{
		response: &Response{ResponseType: InChannel, Text: "deployment complete"},
		handled:  true,
	}
	service := New(executor, nil, nil, plugin)
	args := ExecuteArgs{
		TeamID: "team-1", ChannelID: "channel-1", UserID: "user-1",
		Command: "/deploy production",
	}

	response, err := service.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if response.Post == nil || response.Post.Message != response.Text {
		t.Fatalf("response = %#v", response)
	}
	want := postcommand.Command{
		Source:       postcommand.SourceSlashCommand,
		ActorID:      args.UserID,
		ChannelID:    args.ChannelID,
		Message:      response.Text,
		SlashCommand: "deploy",
	}
	if !reflect.DeepEqual(executor.commands, []postcommand.Command{want}) {
		t.Fatalf("commands = %#v, want %#v", executor.commands, []postcommand.Command{want})
	}
}

func TestPluginEphemeralResponseDoesNotCreatePost(t *testing.T) {
	executor := &slashCommandStub{}
	plugin := &slashPluginStub{
		response: &Response{ResponseType: Ephemeral, Text: "private result"},
		handled:  true,
	}
	service := New(executor, nil, nil, plugin)

	response, err := service.Execute(context.Background(), ExecuteArgs{Command: "/private"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Post != nil || len(executor.commands) != 0 {
		t.Fatalf("response = %#v, commands = %#v", response, executor.commands)
	}
}

func TestPluginHandledNilResponseFailsClosed(t *testing.T) {
	service := New(&slashCommandStub{}, nil, nil, &slashPluginStub{handled: true})

	_, err := service.Execute(context.Background(), ExecuteArgs{Command: "/broken"})
	if err == nil {
		t.Fatal("expected handled nil response to fail")
	}
}
