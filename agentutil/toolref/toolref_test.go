package toolref

import (
	"os"
	"path/filepath"
	"slices"
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

// TestCapabilityVocabularyDoc fails when a vocabulary entry is declared by no
// tool, and keeps docs/tools/capabilities.md in sync. It reads the generated
// agent pages, which each agent's TestToolReference keeps in sync with that
// agent's code; that makes them the only place all agents' taxonomies meet.
// Regenerate with `make docs-tools` (agent pages first, then this page).
func TestCapabilityVocabularyDoc(t *testing.T) {
	declared, err := ScanDeclaredCapabilities("../../docs/tools")
	if err != nil {
		t.Fatalf("scan docs/tools: %v", err)
	}
	if len(declared) == 0 {
		t.Fatal("no capabilities found in docs/tools/*.md; run `make docs-tools`")
	}
	for _, c := range UnusedCapabilities(declared) {
		t.Errorf("capability %q is in the vocabulary but no tool declares it: declare it on a tool or remove it from internal/toolregistry/capabilities.go", c)
	}
	for c := range declared {
		if _, ok := toolregistry.LookupCapability(c); !ok {
			t.Errorf("docs/tools declares %q, which is not in the vocabulary", c)
		}
	}
	CheckGolden(t, "../../docs/tools/capabilities.md", RenderVocabulary(declared))
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
	out, problems := Render(Page{Agent: "a", Tools: tools("check_connection", "cancel_query"), Opts: opts, Path: "x/y.md"})
	if len(problems) != 0 {
		t.Errorf("unexpected problems: %v", problems)
	}
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

func TestGithubSlug(t *testing.T) {
	cases := map[string]string{
		"1.15 `restore_from_backup` — restore from latest pgBackRest backup": "115-restore_from_backup--restore-from-latest-pgbackrest-backup",
		"## 3. Restore from backup after data loss":                          "-3-restore-from-backup-after-data-loss",
		"`check_host`":          "check_host",
		"Operator file uploads": "operator-file-uploads",
		"1. WAL archiving failure (the backup-taking precondition)": "1-wal-archiving-failure-the-backup-taking-precondition",
	}
	for in, want := range cases {
		if got := githubSlug(in); got != want {
			t.Errorf("githubSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseHeadings_SkipsFencesAndNumbersRepeats(t *testing.T) {
	doc := "# Title\n```bash\n# not a heading\n```\n## Setup\n### Setup\n#nospace\n"
	hs := parseHeadings(doc)
	var got []string
	for _, h := range hs {
		got = append(got, h.anchor)
	}
	want := []string{"title", "setup", "setup-1"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("anchors = %v, want %v", got, want)
	}
}

func TestRender_SeeAlso(t *testing.T) {
	root := t.TempDir()
	docs := filepath.Join(root, "docs")
	if err := os.MkdirAll(filepath.Join(docs, "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	doc := "# Mutation tools\n#### 1.2 `cancel_query` — soft interrupt\n#### 1.3 `cancel_query_extra`\n"
	if err := os.WriteFile(filepath.Join(docs, "MUT.md"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := agentutil.CardOptions{SkillCapabilities: map[string][]string{"a-cancel_query": {toolregistry.CapQueryCancel}}}
	page := Page{
		Agent: "a", Tools: tools("cancel_query"), Opts: opts,
		Path: filepath.Join(docs, "tools", "a.md"),
		SeeAlso: map[string][]Link{
			"cancel_query": {{Doc: "MUT.md", Section: "1.2 `cancel_query`"}, {Doc: "MUT.md"}},
		},
	}
	out, problems := Render(page)
	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	for _, want := range []string{
		"  - [MUT.md › 1.2 `cancel_query` — soft interrupt](../MUT.md#12-cancel_query--soft-interrupt)",
		"  - [MUT.md](../MUT.md)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered page missing %q\n%s", want, out)
		}
	}

	bad := page
	bad.SeeAlso = map[string][]Link{
		"cancel_query": {{Doc: "MUT.md", Section: "1."}, {Doc: "MUT.md", Section: "9.9"}, {Doc: "GONE.md"}},
		"not_a_tool":   {{Doc: "MUT.md"}},
	}
	_, problems = Render(bad)
	for _, want := range []string{
		"matches 2 headings in MUT.md",
		`"9.9" matches 0 headings`,
		"see-also document GONE.md",
		`SeeAlso key "not_a_tool" does not match any tool of a`,
	} {
		found := false
		for _, p := range problems {
			if strings.Contains(p, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("want a problem containing %q, got %v", want, problems)
		}
	}
}

func TestScanDeclaredCapabilities_AndUnused(t *testing.T) {
	dir := t.TempDir()
	page := "## `tool_a`\n- **Capabilities:** `logs`, `uptime`\n## `tool_b`\n- **Capabilities:** `logs`\n"
	if err := os.WriteFile(filepath.Join(dir, "x.md"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	// The vocabulary page itself must be ignored.
	if err := os.WriteFile(filepath.Join(dir, "capabilities.md"), []byte("## `zzz`\n- **Capabilities:** `version`\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	declared, err := ScanDeclaredCapabilities(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := declared["logs"]; len(got) != 2 || got[0].Tool != "tool_a" || got[1].Tool != "tool_b" {
		t.Errorf("logs declared by %v", got)
	}
	if _, ok := declared["version"]; ok {
		t.Error("capabilities.md should not be scanned")
	}
	unused := UnusedCapabilities(declared)
	if !slices.Contains(unused, "version") || slices.Contains(unused, "logs") {
		t.Errorf("unused = %v", unused)
	}
}

func TestMissingWriteUps(t *testing.T) {
	// cancel_query is write, terminate_connection destructive, check_connection read.
	p := Page{
		Agent:   "a",
		Tools:   tools("cancel_query", "terminate_connection", "check_connection"),
		SeeAlso: map[string][]Link{"cancel_query": {{Doc: "MUTATION_TOOLS.md"}}},
	}
	got := MissingWriteUps(p)
	if len(got) != 1 || !strings.Contains(got[0], "terminate_connection is classified destructive but links to no write-up") {
		t.Errorf("MissingWriteUps = %v, want only terminate_connection", got)
	}
}
