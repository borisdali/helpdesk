package audit

import (
	"strings"
	"unicode"
)

// Free text that people type into a record (a drill's note, its backup label) is
// later printed to the terminal of whoever reads the record. A terminal obeys the
// control characters in what it prints: an escape sequence can rewrite the screen,
// change the window title or hide text, and a bidirectional override can make one
// word read as another. So such text is refused where it is recorded, and printed
// harmlessly where it is read, because a record can come from a writer that did not
// check it.
//
// The characters are written here by number, never as literals: a source file with
// an invisible override in it is the same attack on whoever reads the code.

// Code points that unicode.IsControl does not cover but that change how text is
// displayed.
const (
	bidiEmbeddingFirst = 0x202A // LRE
	bidiOverrideLast   = 0x202E // RLO, the last of the embeddings and overrides
	bidiIsolateFirst   = 0x2066 // LRI
	bidiIsolateLast    = 0x2069 // PDI
	lineSeparator      = 0x2028
	paragraphSeparator = 0x2029
)

// unsafeRune is a character that must not reach a terminal as it is: a control
// character (C0, DEL and C1, which includes ESC), or one of the bidirectional
// embedding, override and isolate characters, or a line or paragraph separator.
func unsafeRune(r rune) bool {
	switch {
	case unicode.IsControl(r):
		return true
	case r >= bidiEmbeddingFirst && r <= bidiOverrideLast:
		return true
	case r >= bidiIsolateFirst && r <= bidiIsolateLast:
		return true
	case r == lineSeparator || r == paragraphSeparator:
		return true
	}
	return false
}

// HasUnsafeText reports whether s holds a character that must not be recorded
// or printed as it is. With allowLayout, a tab and a newline are allowed: they lay
// text out and do nothing else.
func HasUnsafeText(s string, allowLayout bool) bool {
	for _, r := range s {
		if allowLayout && (r == '\n' || r == '\t') {
			continue
		}
		if unsafeRune(r) {
			return true
		}
	}
	return false
}

// PrintableText returns s with every unsafe character replaced by "?", for text
// that is about to be printed to a terminal. A newline and a tab are kept when
// allowLayout is set. Text with none of them comes back unchanged.
func PrintableText(s string, allowLayout bool) string {
	if !HasUnsafeText(s, allowLayout) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if (allowLayout && (r == '\n' || r == '\t')) || !unsafeRune(r) {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('?')
	}
	return b.String()
}
