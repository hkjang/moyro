package httpapi

import "regexp"

// mentionRE matches Mattermost-style @handles. Handles are limited to the
// same character set the register endpoint accepts (alphanum + . _ -).
// We strip the leading @ when returning matches.
var mentionRE = regexp.MustCompile(`@([a-zA-Z0-9._-]+)`)

// extractMentions pulls unique @-handles out of a message body. Preserves
// first-seen order so tests are deterministic.
func extractMentions(message string) []string {
	hits := mentionRE.FindAllStringSubmatch(message, -1)
	if len(hits) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(hits))
	for _, m := range hits {
		name := m[1]
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}
