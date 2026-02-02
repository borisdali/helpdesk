package main

import "testing"

// --- hasAnomaly tests ---

func TestHasAnomaly(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"error keyword", "Connection error: refused", true},
		{"fail keyword", "Operation failed", true},
		{"refused keyword", "Connection refused by server", true},
		{"timeout keyword", "Request timeout after 10s", true},
		{"too many keyword", "Too many connections", true},
		{"denied keyword", "Permission denied", true},
		{"unreachable keyword", "Host unreachable", true},
		{"crash keyword", "Container crash loop", true},
		{"oom keyword", "OOM killed", true},
		{"killed keyword", "Process killed by signal", true},
		{"case insensitive", "DATABASE ERROR: connection reset", true},
		{"no anomaly", "All systems operational. Health check passed.", false},
		{"empty string", "", false},
		{"partial keyword not matching", "This is a totally fine message", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasAnomaly(tt.text)
			if got != tt.want {
				t.Errorf("hasAnomaly(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

// --- wrapText tests ---

func TestWrapText_ShortLine(t *testing.T) {
	lines := wrapText("hello", 20)
	if len(lines) != 1 || lines[0] != "hello" {
		t.Errorf("wrapText short = %v, want [hello]", lines)
	}
}

func TestWrapText_LongLine(t *testing.T) {
	text := "the quick brown fox jumps over the lazy dog"
	lines := wrapText(text, 20)
	if len(lines) < 2 {
		t.Fatalf("expected wrapping, got %d lines: %v", len(lines), lines)
	}
	for _, line := range lines {
		if len(line) > 20 {
			t.Errorf("line too long (%d chars): %q", len(line), line)
		}
	}
}

func TestWrapText_PreservesNewlines(t *testing.T) {
	text := "line one\nline two\nline three"
	lines := wrapText(text, 80)
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %v", len(lines), lines)
	}
	if lines[0] != "line one" || lines[1] != "line two" || lines[2] != "line three" {
		t.Errorf("lines = %v", lines)
	}
}

func TestWrapText_EmptyLine(t *testing.T) {
	text := "before\n\nafter"
	lines := wrapText(text, 80)
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %v", len(lines), lines)
	}
	if lines[1] != "" {
		t.Errorf("middle line = %q, want empty", lines[1])
	}
}

// --- truncate tests ---

func TestTruncate_WithinLimit(t *testing.T) {
	got := truncate("short", 10)
	if got != "short" {
		t.Errorf("truncate = %q, want %q", got, "short")
	}
}

func TestTruncate_ExactLimit(t *testing.T) {
	got := truncate("12345", 5)
	if got != "12345" {
		t.Errorf("truncate = %q, want %q", got, "12345")
	}
}

func TestTruncate_OverLimit(t *testing.T) {
	got := truncate("hello world", 5)
	if got != "hello..." {
		t.Errorf("truncate = %q, want %q", got, "hello...")
	}
}

// --- displayWidth tests ---

func TestDisplayWidth_ASCII(t *testing.T) {
	got := displayWidth("hello")
	if got != 5 {
		t.Errorf("displayWidth(hello) = %d, want 5", got)
	}
}

func TestDisplayWidth_Empty(t *testing.T) {
	got := displayWidth("")
	if got != 0 {
		t.Errorf("displayWidth('') = %d, want 0", got)
	}
}
