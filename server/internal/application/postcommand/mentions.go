package postcommand

import "regexp"

// mentionRE matches Mattermost-style @handles. Handles use the same character
// set accepted by the registration endpoint (alphanumerics plus . _ -).
var mentionRE = regexp.MustCompile(`@([a-zA-Z0-9._-]+)`)

// ExtractMentions returns unique handles in first-seen order.
func ExtractMentions(message string) []string {
	hits := mentionRE.FindAllStringSubmatch(message, -1)
	if len(hits) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(hits))
	out := make([]string, 0, len(hits))
	for _, match := range hits {
		name := match[1]
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}
