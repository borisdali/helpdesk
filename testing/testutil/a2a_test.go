package testutil

import (
	"testing"

	"github.com/a2aproject/a2a-go/a2a"
)

func TestExtractText_Artifacts(t *testing.T) {
	// ADK agents store their response in Artifacts, not History or Status.Message.
	task := &a2a.Task{
		ID: "task-1",
		Artifacts: []*a2a.Artifact{
			{
				ID:    "artifact-1",
				Parts: a2a.ContentParts{a2a.TextPart{Text: "agent response text"}},
			},
		},
	}

	got := extractText(task)
	if got != "agent response text" {
		t.Errorf("extractText(artifacts) = %q, want %q", got, "agent response text")
	}
}

func TestExtractText_StatusMessage(t *testing.T) {
	// Non-ADK agents or error responses may use Status.Message.
	msg := a2a.NewMessage(a2a.MessageRoleAgent, a2a.TextPart{Text: "error details"})
	task := &a2a.Task{
		ID:     "task-2",
		Status: a2a.TaskStatus{Message: msg},
	}

	got := extractText(task)
	if got != "error details" {
		t.Errorf("extractText(status.message) = %q, want %q", got, "error details")
	}
}

func TestExtractText_History(t *testing.T) {
	// Some implementations populate History.
	task := &a2a.Task{
		ID: "task-3",
		History: []*a2a.Message{
			a2a.NewMessage(a2a.MessageRoleUser, a2a.TextPart{Text: "user turn"}),
			a2a.NewMessage(a2a.MessageRoleAgent, a2a.TextPart{Text: "agent turn"}),
		},
	}

	got := extractText(task)
	if got != "agent turn" {
		t.Errorf("extractText(history) = %q, want %q", got, "agent turn")
	}
}

func TestExtractText_Message(t *testing.T) {
	msg := a2a.NewMessage(a2a.MessageRoleAgent, a2a.TextPart{Text: "direct message"})
	got := extractText(msg)
	if got != "direct message" {
		t.Errorf("extractText(*Message) = %q, want %q", got, "direct message")
	}
}

func TestExtractText_ArtifactsTakePrecedenceOverHistory(t *testing.T) {
	// Artifacts are checked first; if present, history is not used.
	task := &a2a.Task{
		ID: "task-4",
		Artifacts: []*a2a.Artifact{
			{Parts: a2a.ContentParts{a2a.TextPart{Text: "from artifact"}}},
		},
		History: []*a2a.Message{
			a2a.NewMessage(a2a.MessageRoleAgent, a2a.TextPart{Text: "from history"}),
		},
	}

	got := extractText(task)
	if got != "from artifact" {
		t.Errorf("extractText(artifact+history) = %q, want artifact text", got)
	}
}

func TestExtractText_Empty(t *testing.T) {
	task := &a2a.Task{ID: "task-5"}
	got := extractText(task)
	if got != "" {
		t.Errorf("extractText(empty task) = %q, want empty", got)
	}
}
