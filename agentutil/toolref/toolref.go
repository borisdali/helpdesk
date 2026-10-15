// Package toolref validates an agent's tool taxonomy and renders its per-tool
// reference page (docs/tools/<agent>.md).
//
// Each agent package has a test that calls Check with the agent's real tools
// (createTools) and card options. The test fails when a tool has no
// capability, uses a capability outside the closed vocabulary, lacks an
// action class, declares capabilities that contradict its action class, or
// when the committed reference page no longer matches the code. Regenerate
// the pages with `make docs-tools`.
package toolref

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"unicode"

	"google.golang.org/adk/tool"

	"helpdesk/agentutil"
	"helpdesk/internal/audit"
	"helpdesk/internal/toolregistry"
)

// UpdateEnv is the environment variable that makes Check rewrite the
// committed reference page instead of comparing against it.
const UpdateEnv = "HELPDESK_UPDATE_TOOL_DOCS"

// Validate returns every taxonomy problem for the agent's tools. An empty
// result means the taxonomy is complete and consistent.
func Validate(agentName string, tools []tool.Tool, opts agentutil.CardOptions) []string {
	var problems []string
	addf := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	toolNames := make(map[string]bool, len(tools))
	for _, t := range tools {
		toolNames[t.Name()] = true
	}

	for _, t := range tools {
		name := t.Name()
		id := agentName + "-" + name

		class := audit.ClassifyTool(name)
		if class == audit.ActionUnknown {
			addf("%s: no action class: add it to audit.ToolClassification (internal/audit/action.go)", name)
		}

		caps := opts.SkillCapabilities[id]
		if len(caps) == 0 {
			addf("%s: declares no capability: add a SkillCapabilities entry for %q", name, id)
			continue
		}
		seen := make(map[string]bool, len(caps))
		effects := 0
		for _, c := range caps {
			if seen[c] {
				addf("%s: capability %q listed twice", name, c)
			}
			seen[c] = true
			info, ok := toolregistry.LookupCapability(c)
			if !ok {
				addf("%s: capability %q is not in the vocabulary (internal/toolregistry/capabilities.go)", name, c)
				continue
			}
			if info.Kind == toolregistry.KindEffect {
				effects++
			}
		}
		switch class {
		case audit.ActionWrite, audit.ActionDestructive:
			if effects == 0 {
				addf("%s: classified %s but declares no effect capability; say what state it changes", name, class)
			}
		case audit.ActionRead:
			if effects > 0 {
				addf("%s: classified read but declares an effect capability", name)
			}
		}
	}

	// Every taxonomy key must name the root skill or a real tool, so a renamed
	// or removed tool can't leave metadata behind.
	checkKeys := func(field string, keys []string) {
		for _, k := range keys {
			if k == agentName {
				continue
			}
			if !strings.HasPrefix(k, agentName+"-") || !toolNames[strings.TrimPrefix(k, agentName+"-")] {
				addf("%s key %q does not match any tool of %s", field, k, agentName)
			}
		}
	}
	checkKeys("SkillTags", mapKeys(opts.SkillTags))
	checkKeys("SkillExamples", mapKeys(opts.SkillExamples))
	checkKeys("SkillFleetEligible", mapKeys(opts.SkillFleetEligible))
	checkKeys("SkillAutoRemediationEligible", mapKeys(opts.SkillAutoRemediationEligible))
	checkKeys("SkillCapabilities", mapKeys(opts.SkillCapabilities))
	checkKeys("SkillSupersedes", mapKeys(opts.SkillSupersedes))

	// Supersedes targets must exist, and the superseding tool must provide
	// every capability of the tool it replaces (capability parity).
	for _, id := range mapKeys(opts.SkillSupersedes) {
		name := strings.TrimPrefix(id, agentName+"-")
		have := make(map[string]bool)
		for _, c := range opts.SkillCapabilities[id] {
			have[c] = true
		}
		for _, sup := range opts.SkillSupersedes[id] {
			if !toolNames[sup] {
				addf("%s supersedes %q, which is not a tool of %s", name, sup, agentName)
				continue
			}
			for _, c := range opts.SkillCapabilities[agentName+"-"+sup] {
				if !have[c] {
					addf("%s supersedes %s but lacks its capability %q", name, sup, c)
				}
			}
		}
	}

	sort.Strings(problems)
	return problems
}

// Page describes one agent's reference page.
type Page struct {
	Agent string                // full agent name, e.g. "sysadmin_agent"
	Tools []tool.Tool           // the agent's real tools (createTools)
	Opts  agentutil.CardOptions // the agent's real card options (cardOptions)
	// Path is the page's path relative to the test's package directory,
	// e.g. "../../docs/tools/sysadmin.md". Its parent's parent is docs/.
	Path string
	// Notes are added as paragraphs after the page introduction.
	Notes []string
	// SeeAlso maps a tool name to longer write-ups of that tool elsewhere in docs/.
	SeeAlso map[string][]Link
}

// Link points at a section of a document under docs/.
type Link struct {
	Doc string // file name under docs/, e.g. "MUTATION_TOOLS.md"
	// Section is the start of the target heading's text, e.g.
	// "1.15 `restore_from_backup`". It must match exactly one heading.
	// Empty links the document itself.
	Section string
}

type resolvedLink struct {
	text, href string
}

// resolveLinks checks every SeeAlso entry against the real documents and
// returns the rendered links per tool, plus a problem for each tool that
// isn't the agent's, each missing document, and each heading that matches
// zero or several times.
func resolveLinks(p Page) (map[string][]resolvedLink, []string) {
	var problems []string
	docsDir := filepath.Dir(filepath.Dir(p.Path))
	toolNames := make(map[string]bool, len(p.Tools))
	for _, t := range p.Tools {
		toolNames[t.Name()] = true
	}
	headingsByDoc := map[string][]heading{}
	out := map[string][]resolvedLink{}
	for _, name := range mapKeys(p.SeeAlso) {
		if !toolNames[name] {
			problems = append(problems, fmt.Sprintf("SeeAlso key %q does not match any tool of %s", name, p.Agent))
			continue
		}
		for _, l := range p.SeeAlso[name] {
			hs, ok := headingsByDoc[l.Doc]
			if !ok {
				data, err := os.ReadFile(filepath.Join(docsDir, l.Doc))
				if err != nil {
					problems = append(problems, fmt.Sprintf("%s: see-also document %s: %v", name, l.Doc, err))
					continue
				}
				hs = parseHeadings(string(data))
				headingsByDoc[l.Doc] = hs
			}
			if l.Section == "" {
				out[name] = append(out[name], resolvedLink{text: l.Doc, href: "../" + l.Doc})
				continue
			}
			var matches []heading
			for _, h := range hs {
				if strings.HasPrefix(h.text, l.Section) {
					matches = append(matches, h)
				}
			}
			if len(matches) != 1 {
				problems = append(problems, fmt.Sprintf("%s: see-also section %q matches %d headings in %s, want 1", name, l.Section, len(matches), l.Doc))
				continue
			}
			out[name] = append(out[name], resolvedLink{
				text: l.Doc + " › " + matches[0].text,
				href: "../" + l.Doc + "#" + matches[0].anchor,
			})
		}
	}
	return out, problems
}

type heading struct {
	text, anchor string
}

// parseHeadings returns the document's ATX headings outside code fences, with
// the anchors GitHub generates for them (including -1, -2 for repeats).
func parseHeadings(doc string) []heading {
	var hs []heading
	seen := map[string]int{}
	inFence := false
	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence || !strings.HasPrefix(line, "#") {
			continue
		}
		text := strings.TrimLeft(line, "#")
		if len(text) == len(line) || len(line)-len(text) > 6 || !strings.HasPrefix(text, " ") {
			continue
		}
		text = strings.TrimSpace(text)
		a := githubSlug(text)
		if n := seen[a]; n > 0 {
			seen[a] = n + 1
			a = fmt.Sprintf("%s-%d", a, n)
		} else {
			seen[a] = 1
		}
		hs = append(hs, heading{text: text, anchor: a})
	}
	return hs
}

// githubSlug mirrors GitHub's heading-anchor rule: lowercase, drop everything
// except letters, digits, spaces, hyphens and underscores, then turn spaces
// into hyphens.
func githubSlug(text string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(text) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	return b.String()
}

// Render returns the markdown reference page for the agent's tools, plus a
// problem for each see-also link that doesn't resolve.
func Render(p Page) (string, []string) {
	agentName, tools, opts := p.Agent, p.Tools, p.Opts
	short := toolregistry.NormalizeAgentName(agentName)
	schemas := agentutil.ComputeInputSchemas(tools)
	links, problems := resolveLinks(p)

	sorted := make([]tool.Tool, len(tools))
	copy(sorted, tools)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name() < sorted[j].Name() })

	var b strings.Builder
	b.WriteString("<!-- Code generated by `make docs-tools`; DO NOT EDIT. -->\n\n")
	fmt.Fprintf(&b, "# %s agent tools\n\n", titleCase(short))
	fmt.Fprintf(&b, "The `%s` agent (`%s`) publishes %d %s to the [Tool Registry](../TOOL_REGISTRY.md). ",
		short, agentName, len(tools), plural(len(tools), "tool", "tools"))
	b.WriteString("This page is generated from the agent's code: names, descriptions and parameters come from the tool definitions, ")
	b.WriteString("action classes from `audit.ToolClassification`, and capabilities and eligibility from the agent card options. ")
	b.WriteString("Capability names are defined in the [capability vocabulary](capabilities.md).\n\n")
	for _, n := range p.Notes {
		b.WriteString(strings.TrimSpace(n) + "\n\n")
	}

	b.WriteString("| Tool | Action class | Capabilities |\n|---|---|---|\n")
	for _, t := range sorted {
		fmt.Fprintf(&b, "| [`%s`](#%s) | %s | %s |\n", t.Name(), anchor(t.Name()),
			audit.ClassifyTool(t.Name()), codeList(opts.SkillCapabilities[agentName+"-"+t.Name()]))
	}

	for _, t := range sorted {
		name := t.Name()
		id := agentName + "-" + name
		fmt.Fprintf(&b, "\n## `%s`\n\n", name)
		if d := strings.TrimSpace(t.Description()); d != "" {
			b.WriteString(d + "\n\n")
		}
		fmt.Fprintf(&b, "- **Action class:** %s\n", audit.ClassifyTool(name))
		fmt.Fprintf(&b, "- **Capabilities:** %s\n", codeList(opts.SkillCapabilities[id]))
		fmt.Fprintf(&b, "- **Fleet eligible:** %s\n", yesNo(opts.SkillFleetEligible[id]))
		fmt.Fprintf(&b, "- **Auto-remediation eligible:** %s\n", yesNo(opts.SkillAutoRemediationEligible[id]))
		if sup := opts.SkillSupersedes[id]; len(sup) > 0 {
			ls := make([]string, len(sup))
			for i, s := range sup {
				ls[i] = fmt.Sprintf("[`%s`](#%s)", s, anchor(s))
			}
			fmt.Fprintf(&b, "- **Supersedes:** %s\n", strings.Join(ls, ", "))
		}
		if ex := opts.SkillExamples[id]; len(ex) > 0 {
			quoted := make([]string, len(ex))
			for i, e := range ex {
				quoted[i] = "\"" + e + "\""
			}
			fmt.Fprintf(&b, "- **Example prompts:** %s\n", strings.Join(quoted, "; "))
		}
		if ls := links[name]; len(ls) > 0 {
			b.WriteString("- **See also:**\n")
			for _, l := range ls {
				fmt.Fprintf(&b, "  - [%s](%s)\n", l.text, l.href)
			}
		}
		writeParams(&b, schemas[name])
	}
	return b.String(), problems
}

// ToolRef names one tool on one generated agent page.
type ToolRef struct {
	Page string // page file name, e.g. "sysadmin.md"
	Tool string
}

// ScanDeclaredCapabilities reads the generated agent pages in dir and returns,
// for each capability, the tools that declare it. The per-agent
// TestToolReference tests guarantee those pages match the code, so this is
// the cross-agent view of the taxonomy that no single agent package can see.
func ScanDeclaredCapabilities(dir string) (map[string][]ToolRef, error) {
	pages, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return nil, err
	}
	out := map[string][]ToolRef{}
	for _, path := range pages {
		base := filepath.Base(path)
		if base == vocabularyPage {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		current := ""
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "## `") {
				current = strings.Trim(strings.TrimPrefix(line, "## "), "`")
				continue
			}
			if current == "" || !strings.HasPrefix(line, capabilitiesPrefix) {
				continue
			}
			for _, c := range strings.Split(strings.TrimPrefix(line, capabilitiesPrefix), ",") {
				c = strings.Trim(strings.TrimSpace(c), "`")
				if c != "" && c != "—" {
					out[c] = append(out[c], ToolRef{Page: base, Tool: current})
				}
			}
		}
	}
	for c := range out {
		sort.Slice(out[c], func(i, j int) bool {
			if out[c][i].Page != out[c][j].Page {
				return out[c][i].Page < out[c][j].Page
			}
			return out[c][i].Tool < out[c][j].Tool
		})
	}
	return out, nil
}

const (
	vocabularyPage     = "capabilities.md"
	capabilitiesPrefix = "- **Capabilities:** "
)

// UnusedCapabilities returns the vocabulary entries no tool declares.
func UnusedCapabilities(declared map[string][]ToolRef) []string {
	var unused []string
	for _, c := range toolregistry.Vocabulary {
		if len(declared[c.Name]) == 0 {
			unused = append(unused, c.Name)
		}
	}
	return unused
}

// RenderVocabulary returns the markdown page for the capability vocabulary
// (docs/tools/capabilities.md), listing the tools that declare each entry.
func RenderVocabulary(declared map[string][]ToolRef) string {
	var b strings.Builder
	b.WriteString("<!-- Code generated by `make docs-tools`; DO NOT EDIT. -->\n\n")
	b.WriteString("# Capability vocabulary\n\n")
	b.WriteString("Every tool in the [Tool Registry](../TOOL_REGISTRY.md) declares at least one capability from this closed vocabulary ")
	b.WriteString("(defined in `internal/toolregistry/capabilities.go`), and every capability here is declared by at least one tool. ")
	b.WriteString("**Observe** capabilities describe information a tool returns; **effect** capabilities describe state a tool changes. ")
	b.WriteString("A tool classified `write` or `destructive` must declare at least one effect capability, and a `read` tool must declare none.\n\n")
	b.WriteString("Per-agent tool pages: [database](database.md), [Kubernetes](k8s.md), [sysadmin](sysadmin.md), [incident](incident.md), [research](research.md).\n")
	for _, kind := range []toolregistry.CapabilityKind{toolregistry.KindObserve, toolregistry.KindEffect} {
		if kind == toolregistry.KindObserve {
			b.WriteString("\n## Observe\n\n")
		} else {
			b.WriteString("\n## Effect\n\n")
		}
		b.WriteString("| Capability | Domain | Meaning | Declared by |\n|---|---|---|---|\n")
		for _, c := range toolregistry.Vocabulary {
			if c.Kind != kind {
				continue
			}
			refs := make([]string, len(declared[c.Name]))
			for i, r := range declared[c.Name] {
				refs[i] = fmt.Sprintf("[`%s`](%s#%s)", r.Tool, r.Page, anchor(r.Tool))
			}
			by := strings.Join(refs, ", ")
			if by == "" {
				by = "—"
			}
			fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", c.Name, c.Domain, cell(c.Description), by)
		}
	}
	return b.String()
}

// Check validates the agent's taxonomy and see-also links, and compares the
// rendered reference page with the committed file at p.Path. With
// HELPDESK_UPDATE_TOOL_DOCS=1 it rewrites the file instead.
func Check(t *testing.T, p Page) {
	t.Helper()
	if len(p.Tools) == 0 {
		t.Fatalf("%s: no tools to check", p.Agent)
	}
	for _, prob := range Validate(p.Agent, p.Tools, p.Opts) {
		t.Errorf("tool taxonomy: %s", prob)
	}
	content, linkProblems := Render(p)
	for _, prob := range linkProblems {
		t.Errorf("tool see-also: %s", prob)
	}
	CheckGolden(t, p.Path, content)
}

// CheckGolden compares content with the file at path, or rewrites the file
// when HELPDESK_UPDATE_TOOL_DOCS=1.
func CheckGolden(t *testing.T, path, content string) {
	t.Helper()
	if os.Getenv(UpdateEnv) == "1" {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (run `make docs-tools` to generate it)", path, err)
	}
	if string(got) != content {
		t.Errorf("%s is out of date with the code; run `make docs-tools` and commit the result", path)
	}
}

func writeParams(b *strings.Builder, schema map[string]any) {
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		b.WriteString("\nNo parameters.\n")
		return
	}
	required := make(map[string]bool)
	if req, ok := schema["required"].([]any); ok {
		for _, r := range req {
			if s, ok := r.(string); ok {
				required[s] = true
			}
		}
	}
	names := make([]string, 0, len(props))
	for n := range props {
		names = append(names, n)
	}
	// Required parameters first, then alphabetical.
	sort.Slice(names, func(i, j int) bool {
		if required[names[i]] != required[names[j]] {
			return required[names[i]]
		}
		return names[i] < names[j]
	})
	b.WriteString("\n| Parameter | Type | Required | Description |\n|---|---|---|---|\n")
	for _, n := range names {
		p, _ := props[n].(map[string]any)
		desc, _ := p["description"].(string)
		// The schema generator copies the whole `jsonschema:"required,..."` tag
		// into the description; the Required column already says it.
		desc = strings.TrimPrefix(desc, "required,")
		fmt.Fprintf(b, "| `%s` | %s | %s | %s |\n", n, schemaType(p), yesNo(required[n]), cell(desc))
	}
}

func schemaType(p map[string]any) string {
	switch v := p["type"].(type) {
	case string:
		if v == "array" {
			if items, ok := p["items"].(map[string]any); ok {
				if it := schemaType(items); it != "" {
					return it + "[]"
				}
			}
		}
		return v
	case []any:
		parts := make([]string, 0, len(v))
		for _, x := range v {
			if s, ok := x.(string); ok && s != "null" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, " \\| ")
	}
	return ""
}

func cell(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	return strings.ReplaceAll(s, "|", "\\|")
}

func codeList(items []string) string {
	if len(items) == 0 {
		return "—"
	}
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = "`" + s + "`"
	}
	return strings.Join(out, ", ")
}

// anchor mirrors GitHub's heading anchor for a "## `tool_name`" heading.
func anchor(name string) string { return strings.ToLower(name) }

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func titleCase(s string) string {
	if s == "k8s" {
		return "Kubernetes"
	}
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func mapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
