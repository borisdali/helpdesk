package toolref

import (
	"strings"
	"testing"

	"google.golang.org/adk/tool"

	"helpdesk/agentutil"
	"helpdesk/internal/toolregistry"
)

type fakeTool struct{ name string }

func (f fakeTool) Name() string        { return f.name }
func (f fakeTool) Description() string { return "fake " + f.name }
func (f fakeTool) IsLongRunning() bool { return false }

func tools(names ...string) []tool.Tool {
	out := make([]tool.Tool, len(names))
	for i, n := range names {
		out[i] = fakeTool{n}
	}
	return out
}

// TestCapabilityVocabularyDoc keeps docs/tools/capabilities.md in sync with
// toolregistry.Vocabulary. Regenerate with `make docs-tools`.
func TestCapabilityVocabularyDoc(t *testing.T) {
	CheckGolden(t, "../../docs/tools/capabilities.md", RenderVocabulary())
}

func TestVocabulary_Consistent(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range toolregistry.Vocabulary {
		if c.Name == "" || c.Constant == "" || c.Domain == "" || c.Description == "" {
			t.Errorf("incomplete vocabulary entry: %+v", c)
		}
		if c.Kind != toolregistry.KindObserve && c.Kind != toolregistry.KindEffect {
			t.Errorf("%s: unknown kind %q", c.Name, c.Kind)
		}
		if seen[c.Name] {
			t.Errorf("%s listed twice", c.Name)
		}
		seen[c.Name] = true
	}
}

func TestValidate_Clean(t *testing.T) {
	// check_connection is read, cancel_query is write in audit.ToolClassification.
	opts := agentutil.CardOptions{
		SkillCapabilities: map[string][]string{
			"a-check_connection": {toolregistry.CapConnectivity},
			"a-cancel_query":     {toolregistry.CapQueryCancel},
		},
	}
	if p := Validate("a", tools("check_connection", "cancel_query"), opts); len(p) != 0 {
		t.Errorf("unexpected problems: %v", p)
	}
}

func TestValidate_Problems(t *testing.T) {
	cases := []struct {
		name  string
		tools []tool.Tool
		opts  agentutil.CardOptions
		want  string
	}{
		{
			name:  "no capability",
			tools: tools("check_connection"),
			want:  "check_connection: declares no capability",
		},
		{
			name:  "unknown capability",
			tools: tools("check_connection"),
			opts:  agentutil.CardOptions{SkillCapabilities: map[string][]string{"a-check_connection": {"made_up"}}},
			want:  `capability "made_up" is not in the vocabulary`,
		},
		{
			name:  "no action class",
			tools: tools("not_a_classified_tool"),
			opts:  agentutil.CardOptions{SkillCapabilities: map[string][]string{"a-not_a_classified_tool": {toolregistry.CapLogs}}},
			want:  "not_a_classified_tool: no action class",
		},
		{
			name:  "write tool without effect",
			tools: tools("cancel_query"),
			opts:  agentutil.CardOptions{SkillCapabilities: map[string][]string{"a-cancel_query": {toolregistry.CapActiveQueries}}},
			want:  "cancel_query: classified write but declares no effect capability",
		},
		{
			name:  "read tool with effect",
			tools: tools("check_connection"),
			opts:  agentutil.CardOptions{SkillCapabilities: map[string][]string{"a-check_connection": {toolregistry.CapQueryCancel}}},
			want:  "check_connection: classified read but declares an effect capability",
		},
		{
			name:  "stale taxonomy key",
			tools: tools("check_connection"),
			opts: agentutil.CardOptions{
				SkillCapabilities: map[string][]string{"a-check_connection": {toolregistry.CapConnectivity}},
				SkillTags:         map[string][]string{"a-renamed_tool": {"x"}},
			},
			want: `SkillTags key "a-renamed_tool" does not match any tool of a`,
		},
		{
			name:  "supersedes missing tool",
			tools: tools("get_status_summary"),
			opts: agentutil.CardOptions{
				SkillCapabilities: map[string][]string{"a-get_status_summary": {toolregistry.CapUptime}},
				SkillSupersedes:   map[string][]string{"a-get_status_summary": {"gone"}},
			},
			want: `get_status_summary supersedes "gone", which is not a tool of a`,
		},
		{
			name:  "supersedes without capability parity",
			tools: tools("get_status_summary", "get_server_info"),
			opts: agentutil.CardOptions{
				SkillCapabilities: map[string][]string{
					"a-get_status_summary": {toolregistry.CapUptime},
					"a-get_server_info":    {toolregistry.CapUptime, toolregistry.CapVersion},
				},
				SkillSupersedes: map[string][]string{"a-get_status_summary": {"get_server_info"}},
			},
			want: `get_status_summary supersedes get_server_info but lacks its capability "version"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			problems := Validate("a", tc.tools, tc.opts)
			for _, p := range problems {
				if strings.Contains(p, tc.want) {
					return
				}
			}
			t.Errorf("want a problem containing %q, got %v", tc.want, problems)
		})
	}
}

func TestRender_ListsEveryTool(t *testing.T) {
	opts := agentutil.CardOptions{
		SkillCapabilities:  map[string][]string{"a-check_connection": {toolregistry.CapConnectivity}},
		SkillFleetEligible: map[string]bool{"a-check_connection": true},
	}
	out := Render("a", tools("check_connection", "cancel_query"), opts)
	for _, want := range []string{
		"## `check_connection`", "## `cancel_query`",
		"| [`check_connection`](#check_connection) | read | `connectivity` |",
		"- **Fleet eligible:** yes",
		"fake cancel_query",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered page missing %q", want)
		}
	}
}
