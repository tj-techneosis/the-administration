package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// What onboarding takes away. An agent's own file names the tools it was
// written for (`tools: Bash, Read, ...`); the kind it joins as gets a fixed
// set. Whatever the agent had and its kind doesn't give it, it can no longer
// do here, and the person must hear that, every time, whether the Director
// remembered to say so or not: a critic that ran probes and joins read-only
// misses what only running finds.

// ownTools is the tools line of an agent file, as written; nil when the file
// names none (it then had everything, and says nothing about what it used).
func ownTools(b []byte) []string {
	fm, _, err := splitFrontmatter(b)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(fm), "\n")
	for i, l := range lines {
		k, v, ok := strings.Cut(l, ":")
		if !ok || strings.TrimSpace(k) != "tools" || strings.HasPrefix(l, " ") {
			continue
		}
		var out []string
		if v = strings.TrimSpace(v); v != "" {
			for _, t := range strings.Split(strings.Trim(v, "[]"), ",") {
				if t = strings.Trim(strings.TrimSpace(t), `"'`); t != "" {
					out = append(out, t)
				}
			}
			return out
		}
		for _, n := range lines[i+1:] {
			t, ok := strings.CutPrefix(strings.TrimSpace(n), "- ")
			if !ok {
				break
			}
			out = append(out, strings.Trim(strings.TrimSpace(t), `"'`))
		}
		return out
	}
	return nil
}

var toolArgs = regexp.MustCompile(`^([A-Za-z_]+)\(.*\)$`)

// lostBy is what of tools a professional of the kind, with that equipment
// (sandboxed: the pieces of it that give a shell in the OS sandbox),
// doesn't get, each with what it means.
func lostBy(tools []string, kind string, uses, sandboxed []string) []string {
	var lost []string
	add := func(t, what string) { lost = append(lost, t+": "+what) }
	for _, t := range tools {
		base := t
		if m := toolArgs.FindStringSubmatch(t); m != nil {
			base = m[1]
		}
		switch {
		case base == "Read" || base == "Grep" || base == "Glob":
			if kind == "user" {
				add(t, "no files of the project, only its handout")
			}
		case base == "Bash":
			switch kind {
			case "critic":
				if len(uses) > 0 {
					add(t, "runs only what "+strings.Join(uses, ", ")+" gives it")
				} else {
					add(t, "runs nothing: no probes, scripts, tests or database queries")
				}
			default:
				if len(sandboxed) > 0 {
					add(t, "a shell only for "+strings.Join(sandboxed, ", ")+"'s commands, in the OS sandbox")
				} else if len(uses) > 0 {
					add(t, "no shell; only "+strings.Join(uses, ", ")+"'s tools")
				} else {
					add(t, "no shell: nothing it ran itself (the database, a script, a second session)")
				}
			}
		case base == "Write" || base == "Edit" || base == "NotebookEdit" || base == "MultiEdit":
			if len(sandboxed) > 0 {
				add(t, "writes only in its desk")
			} else {
				add(t, "writes nothing")
			}
		case strings.HasPrefix(t, "mcp__playwright__"):
			name := strings.TrimPrefix(t, "mcp__playwright__")
			if kind != "user" {
				add(t, "no browser")
			} else if !slices.Contains(browserTools, name) {
				add(t, "not in the browser's allowlist")
			}
		case strings.HasPrefix(t, "mcp__"):
			add(t, "no such server here (only the project's equipment)")
		default:
			// WebFetch, WebSearch, Agent and anything else no kind has.
			add(t, "not given to any professional")
		}
	}
	return lost
}

// onboardLosses reads, after an onboard run, what each agent joined as and
// what it lost doing so: one line per agent that lost anything.
func onboardLosses(project, in, out string) []string {
	kinds := map[string]string{}
	uses := map[string][]string{}
	var cfg projectConfig
	if _, err := toml.DecodeFile(filepath.Join(out, "roster.toml"), &cfg); err == nil {
		for name, cr := range cfg.Critics {
			kinds[name], uses[name] = "critic", cr.Uses
		}
	}
	convs, _ := filepath.Glob(filepath.Join(out, "convert", "*.toml"))
	for _, f := range convs {
		var cv conversion
		if _, err := toml.DecodeFile(f, &cv); err == nil {
			name := strings.TrimSuffix(filepath.Base(f), ".toml")
			kinds[name], uses[name] = cv.Kind, cv.Uses
		}
	}
	names := make([]string, 0, len(kinds))
	for n := range kinds {
		names = append(names, n)
	}
	sort.Strings(names)
	var lines []string
	for _, name := range names {
		b, err := os.ReadFile(filepath.Join(in, "agents", name+".md"))
		if err != nil {
			continue
		}
		var sandboxed []string
		for _, u := range uses[name] {
			if eq, err := checkEquipment(project, u); err == nil && eq.Server == "sandboxed" {
				sandboxed = append(sandboxed, u)
			}
		}
		if lost := lostBy(ownTools(b), kinds[name], uses[name], sandboxed); len(lost) > 0 {
			lines = append(lines, fmt.Sprintf("%s (joins as a %s): %s", name, kinds[name], strings.Join(lost, "; ")))
		}
	}
	return lines
}

// toolsNote tells the Director, before it decides, which agents were written
// for tools beyond reading: the ones to weigh what they'd lose.
func toolsNote(b *strings.Builder, agents string) {
	files, _ := filepath.Glob(filepath.Join(agents, "*.md"))
	sort.Strings(files)
	var lines []string
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		t := ownTools(src)
		if len(lostBy(t, "critic", nil, nil)) > 0 {
			lines = append(lines, fmt.Sprintf("- %s: %s", strings.TrimSuffix(filepath.Base(f), ".md"), strings.Join(t, ", ")))
		}
	}
	if len(lines) == 0 {
		return
	}
	fmt.Fprintf(b, "These agents' own files name tools beyond reading. For each, say in report.md what it can't do in the kind you give it, and when its body relies on running something (probes, scripts, tests, the database), plan equipment that gives that back in ./out/equipment.md:\n%s\n", strings.Join(lines, "\n"))
}
