package postcommand

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestExtractMentionsCandidates(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    []string
	}{
		{
			name:    "sentence punctuation keeps the raw and trimmed handle",
			message: "cc @alice.",
			want:    []string{"alice.", "alice"},
		},
		{
			name:    "repeated trailing punctuation collapses to one trimmed handle",
			message: "thanks @build.bot.-_",
			want:    []string{"build.bot.-_", "build.bot"},
		},
		{
			name:    "mixed case offers the lowercase form registration stores",
			message: "ping @Alice and @BUILD_Bot",
			want:    []string{"Alice", "alice", "BUILD_Bot", "build_bot"},
		},
		{
			name:    "email addresses are not mentions",
			message: "mail ops@example.com for access",
			want:    nil,
		},
		{
			name:    "url handles inside a path are not mentions",
			message: "see https://moyro.local/x@alice",
			want:    nil,
		},
		{
			name:    "handles must start with an alphanumeric",
			message: "@.alice @_bob @-carol @dave",
			want:    []string{"dave"},
		},
		{
			name:    "markdown and korean text still delimit a handle",
			message: "**@alice** 안녕@bob (@carol)",
			want:    []string{"alice", "bob", "carol"},
		},
		{
			name:    "duplicate candidates are reported once in first-seen order",
			message: "@alice @alice. @Alice",
			want:    []string{"alice", "alice.", "Alice"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ExtractMentions(test.message); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("mentions = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestExtractMentionsBoundsCandidateCount(t *testing.T) {
	message := strings.Repeat("@user ", maxMentionCandidates*2)
	got := ExtractMentions(message)
	if len(got) != 1 {
		t.Fatalf("repeated handle produced %d candidates, want 1", len(got))
	}

	var builder strings.Builder
	for index := 0; index < maxMentionCandidates*2; index++ {
		fmt.Fprintf(&builder, "@user%d. ", index)
	}
	if got := len(ExtractMentions(builder.String())); got != maxMentionCandidates {
		t.Fatalf("candidate count = %d, want the %d cap", got, maxMentionCandidates)
	}
}
