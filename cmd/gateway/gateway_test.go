package main

import (
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/a2a"
)

// --- extractText tests ---

func TestExtractText_Empty(t *testing.T) {
	got := extractText(nil)
	if got != "" {
		t.Errorf("extractText(nil) = %q, want empty", got)
	}
}

func TestExtractText_SingleTextPart(t *testing.T) {
	parts := a2a.ContentParts{a2a.TextPart{Text: "hello"}}
	got := extractText(parts)
	if got != "hello" {
		t.Errorf("extractText = %q, want %q", got, "hello")
	}
}

func TestExtractText_MultipleTextParts(t *testing.T) {
	parts := a2a.ContentParts{
		a2a.TextPart{Text: "line1"},
		a2a.TextPart{Text: "line2"},
	}
	got := extractText(parts)
	if got != "line1\nline2" {
		t.Errorf("extractText = %q, want %q", got, "line1\nline2")
	}
}

func TestExtractText_NonTextPartsIgnored(t *testing.T) {
	parts := a2a.ContentParts{
		a2a.TextPart{Text: "hello"},
		a2a.DataPart{Data: map[string]any{"key": "val"}},
		a2a.TextPart{Text: "world"},
	}
	got := extractText(parts)
	if got != "hello\nworld" {
		t.Errorf("extractText = %q, want %q", got, "hello\nworld")
	}
}

// --- buildToolPrompt tests ---

func TestBuildToolPrompt_NoArgs(t *testing.T) {
	got := buildToolPrompt("check_connection", nil)
	if got != "Call the check_connection tool." {
		t.Errorf("buildToolPrompt = %q", got)
	}
}

func TestBuildToolPrompt_WithArgs(t *testing.T) {
	got := buildToolPrompt("get_pods", map[string]any{
		"namespace": "default",
	})
	if !strings.Contains(got, "Call the get_pods tool") {
		t.Errorf("missing tool name in prompt: %q", got)
	}
	if !strings.Contains(got, "namespace=default") {
		t.Errorf("missing arg in prompt: %q", got)
	}
}

// --- extractResponse tests ---

func TestExtractResponse_TaskWithStatusMessage(t *testing.T) {
	task := &a2a.Task{
		ID: "task-1",
		Status: a2a.TaskStatus{
			State: a2a.TaskStateCompleted,
			Message: &a2a.Message{
				Role:  a2a.MessageRoleAgent,
				Parts: a2a.ContentParts{a2a.TextPart{Text: "done"}},
			},
		},
	}

	resp := extractResponse(task)
	if resp.TaskID != "task-1" {
		t.Errorf("TaskID = %q, want %q", resp.TaskID, "task-1")
	}
	if resp.State != "completed" {
		t.Errorf("State = %q, want %q", resp.State, "completed")
	}
	if resp.Text != "done" {
		t.Errorf("Text = %q, want %q", resp.Text, "done")
	}
}

func TestExtractResponse_TaskWithHistory(t *testing.T) {
	task := &a2a.Task{
		ID: "task-2",
		Status: a2a.TaskStatus{
			State: a2a.TaskStateCompleted,
		},
		History: []*a2a.Message{
			{Role: a2a.MessageRoleUser, Parts: a2a.ContentParts{a2a.TextPart{Text: "request"}}},
			{Role: a2a.MessageRoleAgent, Parts: a2a.ContentParts{a2a.TextPart{Text: "response"}}},
		},
	}

	resp := extractResponse(task)
	if resp.Text != "response" {
		t.Errorf("Text = %q, want %q (from history)", resp.Text, "response")
	}
}

func TestExtractResponse_TaskWithArtifactFallback(t *testing.T) {
	task := &a2a.Task{
		ID: "task-3",
		Status: a2a.TaskStatus{
			State: a2a.TaskStateCompleted,
		},
		Artifacts: []*a2a.Artifact{
			{
				ID:    "art-1",
				Name:  "report",
				Parts: a2a.ContentParts{a2a.TextPart{Text: "artifact text"}},
			},
		},
	}

	resp := extractResponse(task)
	if resp.Text != "artifact text" {
		t.Errorf("Text = %q, want %q (from artifact fallback)", resp.Text, "artifact text")
	}
	if len(resp.Artifacts) != 1 {
		t.Errorf("Artifacts len = %d, want 1", len(resp.Artifacts))
	}
}

func TestExtractResponse_Message(t *testing.T) {
	msg := &a2a.Message{
		Role:  a2a.MessageRoleAgent,
		Parts: a2a.ContentParts{a2a.TextPart{Text: "direct message"}},
	}

	resp := extractResponse(msg)
	if resp.Text != "direct message" {
		t.Errorf("Text = %q, want %q", resp.Text, "direct message")
	}
	if resp.TaskID != "" {
		t.Errorf("TaskID = %q, want empty for Message type", resp.TaskID)
	}
}
