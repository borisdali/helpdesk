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

	got, _ := extractResponse(task)
	if got != "agent response text" {
		t.Errorf("extractResponse(artifacts) = %q, want %q", got, "agent response text")
	}
}

func TestExtractText_StatusMessage(t *testing.T) {
	// Non-ADK agents or error responses may use Status.Message.
	msg := a2a.NewMessage(a2a.MessageRoleAgent, a2a.TextPart{Text: "error details"})
	task := &a2a.Task{
		ID:     "task-2",
		Status: a2a.TaskStatus{Message: msg},
	}

	got, _ := extractResponse(task)
	if got != "error details" {
		t.Errorf("extractResponse(status.message) = %q, want %q", got, "error details")
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

	got, _ := extractResponse(task)
	if got != "agent turn" {
		t.Errorf("extractResponse(history) = %q, want %q", got, "agent turn")
	}
}

func TestExtractText_Message(t *testing.T) {
	msg := a2a.NewMessage(a2a.MessageRoleAgent, a2a.TextPart{Text: "direct message"})
	got, _ := extractResponse(msg)
	if got != "direct message" {
		t.Errorf("extractResponse(*Message) = %q, want %q", got, "direct message")
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

	got, _ := extractResponse(task)
	if got != "from artifact" {
		t.Errorf("extractResponse(artifact+history) = %q, want artifact text", got)
	}
}

func TestExtractText_Empty(t *testing.T) {
	task := &a2a.Task{ID: "task-5"}
	got, _ := extractResponse(task)
	if got != "" {
		t.Errorf("extractResponse(empty task) = %q, want empty", got)
	}
}

func TestExtractText_ToolCallSummary(t *testing.T) {
	// ADK agents emit a DataPart with helpdesk_type="tool_call_summary".
	task := &a2a.Task{
		ID: "task-6",
		Artifacts: []*a2a.Artifact{
			{
				Parts: a2a.ContentParts{
					a2a.TextPart{Text: "agent response"},
					a2a.DataPart{
						Data: map[string]any{"tool_calls": []any{"check_connection", "get_database_info"}},
						Metadata: map[string]any{"helpdesk_type": "tool_call_summary"},
					},
				},
			},
		},
	}

	text, toolCalls := extractResponse(task)
	if text != "agent response" {
		t.Errorf("text = %q, want %q", text, "agent response")
	}
	if len(toolCalls) != 2 {
		t.Fatalf("len(toolCalls) = %d, want 2", len(toolCalls))
	}
	if toolCalls[0].Name != "check_connection" {
		t.Errorf("toolCalls[0].Name = %q, want %q", toolCalls[0].Name, "check_connection")
	}
	if toolCalls[1].Name != "get_database_info" {
		t.Errorf("toolCalls[1].Name = %q, want %q", toolCalls[1].Name, "get_database_info")
	}
}

func TestExtractText_ToolCallSuccess(t *testing.T) {
	// Success is false when the error sentinel appears in response text.
	task := &a2a.Task{
		ID: "task-7",
		Artifacts: []*a2a.Artifact{
			{
				Parts: a2a.ContentParts{
					a2a.TextPart{Text: "error — check_connection failed to connect"},
					a2a.DataPart{
						Data:     map[string]any{"tool_calls": []any{"check_connection"}},
						Metadata: map[string]any{"helpdesk_type": "tool_call_summary"},
					},
				},
			},
		},
	}

	_, toolCalls := extractResponse(task)
	if len(toolCalls) != 1 {
		t.Fatalf("len(toolCalls) = %d, want 1", len(toolCalls))
	}
	if toolCalls[0].Success {
		t.Error("Success should be false when error sentinel present in response text")
	}
}
