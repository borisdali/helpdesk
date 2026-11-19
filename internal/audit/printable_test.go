package audit

import "testing"

// The dangerous characters are built from their numbers, never written as
// literals: a source file with an invisible override in it is the same attack.
var (
	esc      = string(rune(0x1b))
	bel      = string(rune(0x07))
	cr       = string(rune(0x0d))
	nul      = string(rune(0x00))
	del      = string(rune(0x7f))
	c1csi    = string(rune(0x9b))
	rlo      = string(rune(0x202E))
	lri      = string(rune(0x2066))
	lineSep  = string(rune(0x2028))
	paraSep  = string(rune(0x2029))
	zwj      = string(rune(0x200D))
	backspc  = string(rune(0x08))
	allEvils = esc + "[2J" + esc + "]0;t" + bel + cr + nul + rlo + lri + c1csi + lineSep
)

func TestHasUnsafeText(t *testing.T) {
	for name, c := range map[string]struct {
		s      string
		layout bool
		unsafe bool
	}{
		"plain":                   {"Q4 drill, new runbook", false, false},
		"accents and CJK":         {"Übung ✓", false, false},
		"zero-width joiner":       {"a" + zwj + "b drill", false, false}, // a joiner is not a control character
		"escape":                  {"before" + esc + "[2Jafter", true, true},
		"escape to a title":       {esc + "]0;pwned" + bel, true, true},
		"bell":                    {"a" + bel + "b", true, true},
		"carriage return":         {"fake" + cr + "real", true, true},
		"backspace":               {"a" + backspc + "b", true, true},
		"NUL":                     {"a" + nul + "b", true, true},
		"DEL":                     {"a" + del + "b", true, true},
		"C1 control":              {"a" + c1csi + "b", true, true},
		"right-to-left override":  {"gnp." + rlo + "exe", true, true},
		"left-to-right isolate":   {"a" + lri + "b", true, true},
		"line separator":          {"a" + lineSep + "b", true, true},
		"paragraph separator":     {"a" + paraSep + "b", true, true},
		"newline, layout allowed": {"line one\nline two", true, false},
		"tab, layout allowed":     {"a\tb", true, false},
		"newline, layout refused": {"line one\nline two", false, true},
		"tab, layout refused":     {"a\tb", false, true},
		"empty":                   {"", false, false},
	} {
		if got := HasUnsafeText(c.s, c.layout); got != c.unsafe {
			t.Errorf("%s: HasUnsafeText(%q, %v) = %v, want %v", name, c.s, c.layout, got, c.unsafe)
		}
	}
}

func TestPrintableText(t *testing.T) {
	for name, c := range map[string]struct {
		in     string
		layout bool
		want   string
	}{
		"unchanged":           {"nothing odd here ✓", false, "nothing odd here ✓"},
		"escape sequence":     {"a" + esc + "[31mred" + esc + "[0m", false, "a?[31mred?[0m"},
		"title change":        {esc + "]0;x" + bel + "done", false, "?]0;x?done"},
		"override":            {"gnp." + rlo + "exe", false, "gnp.?exe"},
		"keeps layout":        {"one\n\ttwo" + esc, true, "one\n\ttwo?"},
		"replaces layout too": {"one\ntwo", false, "one?two"},
		"empty":               {"", true, ""},
	} {
		if got := PrintableText(c.in, c.layout); got != c.want {
			t.Errorf("%s: PrintableText(%q) = %q, want %q", name, c.in, got, c.want)
		}
	}
	// Nothing in the result can act on a terminal.
	for _, in := range []string{allEvils, "mixed " + esc + " ok " + lineSep + " text"} {
		if out := PrintableText(in, false); HasUnsafeText(out, false) {
			t.Errorf("PrintableText(%q) = %q still has an unsafe character", in, out)
		}
	}
}
