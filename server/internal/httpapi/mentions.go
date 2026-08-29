package httpapi

import "github.com/hkjang/moyro/server/internal/application/postcommand"

// extractMentions pulls unique @-handles out of a message body. Preserves
// the legacy package-local helper for compatibility with focused HTTP tests;
// the create-post application service owns the implementation now.
func extractMentions(message string) []string {
	return postcommand.ExtractMentions(message)
}
