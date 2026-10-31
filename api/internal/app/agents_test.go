package app_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TP-122: the agents in .claude/agents run unattended, so a renamed tool or a missing rule must fail
// `just test`, not an agent at 3am. Each file must have well-formed frontmatter, name only MCP tools
// the server really registers, read the key goals if it uses the tracker, and say which actor it
// records as if it writes to the tracker.
func TestEveryAgentIsWellFormedAndNamesOnlyRealTools(t *testing.T) {
	h := router(t)
	listed := mcpSession(t, h)(`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	registered := map[string]bool{}
	for _, m := range regexp.MustCompile(`"name":"([a-z_]+)"`).FindAllStringSubmatch(listed, -1) {
		registered[m[1]] = true
	}
	if len(registered) < 20 {
		t.Fatalf("read only %d tools from tools/list; has the MCP listing changed shape?", len(registered))
	}

	files, err := filepath.Glob("../../../.claude/agents/*.md")
	if err != nil || len(files) == 0 {
		t.Fatalf("no agent files found (%v)", err)
	}
	writes := []string{"plan_batch", "add_to_batch", "record_review", "report_findings", "record_verdict", "update_ticket",
		"finish_batch", "add_comment", "record_decision"}
	toolRef := regexp.MustCompile(`mcp__ticketing__([a-z_]+)`)
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		name := strings.TrimSuffix(filepath.Base(file), ".md")
		parts := strings.SplitN(text, "---", 3)
		if len(parts) < 3 || strings.TrimSpace(parts[0]) != "" {
			t.Errorf("%s: no frontmatter between --- lines", name)
			continue
		}
		front, body := parts[1], parts[2]
		if !regexp.MustCompile(`(?m)^name: ` + regexp.QuoteMeta(name) + `\s*$`).MatchString(front) {
			t.Errorf("%s: frontmatter name does not match the file name", name)
		}
		if !regexp.MustCompile(`(?m)^description: \S`).MatchString(front) {
			t.Errorf("%s: no description", name)
		}
		var used []string
		for _, m := range toolRef.FindAllStringSubmatch(text, -1) {
			if !registered[m[1]] {
				t.Errorf("%s names mcp__ticketing__%s, which the MCP server does not register", name, m[1])
			}
			used = append(used, m[1])
		}
		if len(used) == 0 {
			continue // an agent that never touches the tracker has nothing more to prove
		}
		if !strings.Contains(body, "key-goals") {
			t.Errorf("%s uses the tracker but never reads the key goals (key-goals)", name)
		}
		sort.Strings(used)
		for _, w := range writes {
			if i := sort.SearchStrings(used, w); i < len(used) && used[i] == w {
				if !strings.Contains(body, "record as") && !strings.Contains(body, "X-Actor") {
					t.Errorf("%s writes to the tracker (%s) but never says which actor it records as", name, w)
				}
				break
			}
		}
	}
}

// TP-147: the loop points these role files at whatever project it is working, passing the project and
// its checkout in every prompt (scripts/loop.sh), so a project name, one of its directories or one of
// its build commands written into a role file is a bug twice over: it aims an agent working project B
// at project A, and it makes the role file a second, unversioned copy of what the guidance already
// says. Stack-specific text belongs in that project's guidance, which claim_next_ticket attaches.
//
// Deliberately a second test: TestEveryAgentIsWellFormedAndNamesOnlyRealTools is a confirmed tripwire,
// so editing it would change its hash and clear that confirmation.
func TestNoAgentHardCodesAProjectOrItsPaths(t *testing.T) {
	files, err := filepath.Glob("../../../.claude/agents/*.md")
	if err != nil || len(files) == 0 {
		t.Fatalf("no agent files found (%v)", err)
	}
	// The tracker's MCP tools are named after the server, not after any project, and its REST
	// endpoints are paths on the API rather than directories in a checkout. Neither is hard-coding.
	exempt := regexp.MustCompile(`mcp__ticketing__[a-z_]+|/api/[a-z-]+`)
	banned := []struct {
		why string
		re  *regexp.Regexp
	}{
		{"names a project", regexp.MustCompile(`(?i)ticketing`)},
		{"names a directory in one checkout", regexp.MustCompile(`\b(api|web|docs|internal|migrations|scripts|eval)/`)},
		{"names one repository's build tool", regexp.MustCompile(`(?i)justfile|\bjust (lint|test|build|dev|generate|loop|check|guidance|selftest|seed|setup|schema|fmt|run)\b`)},
		{"names one stack's tools", regexp.MustCompile(`(?i)\b(gofmt|gqlgen|pgx|deadcode|npm|npx|vitest|solidjs|tanstack|vite)\b|\bgo (vet|test|run|build)\b|\bsolid 2\b`)},
		{"names a language-specific guidance document another project will not have", regexp.MustCompile(`go-service-layer|the-web-app`)},
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		name := strings.TrimSuffix(filepath.Base(file), ".md")
		text := exempt.ReplaceAllString(string(raw), "")
		for _, b := range banned {
			if hit := b.re.FindString(text); hit != "" {
				t.Errorf("%s %s (%q): the project comes from the prompt and its conventions from get_guidance, "+
					"so this belongs in that project's guidance, not in a role file", name, b.why, hit)
			}
		}
	}
}

// TP-244: what only Joel may do is written once, in the-agents guidance. Every role file points at
// that list exactly once and keeps no copy of it, so the list cannot drift into disagreeing copies.
func TestEveryAgentPointsAtTheOnlyJoelListOnce(t *testing.T) {
	files, err := filepath.Glob("../../../.claude/agents/*.md")
	if err != nil || len(files) == 0 {
		t.Fatalf("no agent files found (%v)", err)
	}
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(string(body), "Only Joel"); n != 1 {
			t.Errorf("%s names the Only Joel list %d times, want exactly 1 pointer", filepath.Base(f), n)
		}
	}
}
