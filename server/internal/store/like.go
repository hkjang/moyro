package store

import "strings"

// Every search endpoint in this server answers a user-typed term with a
// `LIKE`/`ILIKE` match, and the term was being pasted into the pattern raw.
// That silently reinterprets three characters the user meant literally:
//
//	%  matches any run of characters, so a term of "%" matches every row and
//	   looking for a channel literally named "50% 할인" also returns everything
//	_  matches exactly one character, so "john_doe" also finds "johnXdoe"
//	\  escapes whatever follows it, so a term ending in a backslash eats the
//	   trailing wildcard we appended and turns "contains" into "ends with"
//
// The escape character below is a plain backslash because that is PostgreSQL's
// default for LIKE — no `ESCAPE` clause is needed at the call sites, and adding
// one would only restate the default.
//
// The replacer runs left to right over the input and never rescans what it
// wrote, so escaping the backslash in the same pass as the wildcards is safe:
// the backslash it emits cannot be picked up as a fresh match.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// EscapeLike quotes the LIKE/ILIKE metacharacters in a user-supplied term so
// the term matches itself and nothing else. Callers that need the usual
// "contains" or "prefix" shapes should reach for LikeContains / LikePrefix
// instead of concatenating the wildcards by hand.
func EscapeLike(term string) string { return likeEscaper.Replace(term) }

// LikeContains builds the `%term%` pattern for a substring search.
func LikeContains(term string) string { return "%" + EscapeLike(term) + "%" }

// LikePrefix builds the `term%` pattern used to rank prefix hits ahead of
// mid-word ones.
func LikePrefix(term string) string { return EscapeLike(term) + "%" }
