// Package logsafe neutralises untrusted text before it is written to a log
// line. It was extracted from two copies that had drifted apart: the auth
// handler version dropped control characters and escaped brackets, while the
// dashboard copy replaced tabs/newlines with a space and left brackets alone,
// so a crafted plugin event could still forge a "[WARN]" marker in
// dashboard.log (audit finding L-11).
package logsafe

import "strings"

// Field neutralises untrusted text for logging.
//
// Two classes of injection are handled:
//
//  1. Line breaking / control characters. CR, LF, tab and other C0/C1 controls
//     (NUL, ESC, ANSI introducers, ...) are DROPPED, not replaced with a space.
//     Replacing them with a space still leaves a readable forged fragment such
//     as "user=a [INFO] FORGED from=...", which a log parser can mistake for a
//     real entry.
//
//  2. Log-parser metacharacters. Brackets are the marker a parser (and a human)
//     uses to spot a level tag ([INFO], [WARN], ...). They are escaped with a
//     backslash so an injected "[INFO] FORGED" can never look like a real log
//     level.
//
// The value is only used for logging; authentication and matching must keep
// using the raw input.
func Field(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			// Drop the character entirely: a space would still separate a
			// forged "[INFO]" tag from the surrounding text.
			continue
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
			// Drop other control characters (NUL, ESC, ANSI introducers, ...).
			continue
		case r == '[' || r == ']':
			// Escape the bracket so an injected "[INFO]"/"[WARN]" tag cannot
			// be read as a real log level by a parser or by a human.
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
