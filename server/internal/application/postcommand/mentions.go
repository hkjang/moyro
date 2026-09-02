package postcommand

import (
	"regexp"
	"strings"
)

// mentionRE matches Mattermost-style @handles. Handles use the same character
// set accepted by the registration endpoint (alphanumerics plus . _ -) and must
// start with an alphanumeric. The leading `\B` keeps the handle from starting
// mid-word, so an email address or a URL fragment ("ops@example.com") no longer
// looks like a mention of "example.com".
var mentionRE = regexp.MustCompile(`\B@([a-zA-Z0-9][a-zA-Z0-9._-]*)`)

// maxMentionCandidates bounds the name list handed to the username lookup so a
// message stuffed with handles cannot turn one post into an unbounded query.
const maxMentionCandidates = 200

// ExtractMentions returns unique candidate handles in first-seen order. A
// handle can appear in the message in a form the users table does not store
// verbatim, so a single @token may contribute more than one candidate:
//
//   - Trailing "._-" is kept and also offered trimmed, because a mention that
//     ends a sentence ("cc @alice.") otherwise resolves to nobody.
//   - Mixed case is kept and also offered lowercased, because registration
//     stores usernames lowercased while bot and identity provisioning preserve
//     whatever case the caller supplied.
//
// Callers resolve the list against the users table and silently drop the
// candidates that match no account.
func ExtractMentions(message string) []string {
	hits := mentionRE.FindAllStringSubmatch(message, -1)
	if len(hits) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(hits))
	out := make([]string, 0, len(hits))
	add := func(name string) {
		if name == "" || len(out) >= maxMentionCandidates {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	for _, match := range hits {
		for name := match[1]; name != ""; {
			add(name)
			if lowered := strings.ToLower(name); lowered != name {
				add(lowered)
			}
			trimmed := strings.TrimRight(name, "._-")
			if trimmed == name {
				break
			}
			name = trimmed
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
