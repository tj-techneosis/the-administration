package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

// The house ships inside the binary. Today it holds only the suggested open
// rooms: the framework hires nobody, each project trains its own. A house
// professional dropped in here would only ever work inside a contract (a file
// in .claude/agents would be picked up by every ordinary session).
//
//go:embed house
var house embed.FS

const defaultCoins = 10 * time.Minute

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// A professional is an agent file with a administration: block in its
// frontmatter. Agents a project already has join as critics (see
// roster.toml) with no edits to their files.
type professional struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Kind        string        `json:"kind"` // "user" or "critic"
	Browser     bool          `json:"browser"`
	Coins       time.Duration `json:"-"`
	CoinsMin    float64       `json:"coins_min"`
	SignsInAs   string        `json:"signs_in_as,omitempty"`
	Sees        string        `json:"sees,omitempty"` // users: "screen" (the default) or "screen-reader"
	Knows       []string      `json:"knows"`
	Constraints []string      `json:"constraints,omitempty"`
	Uses        []string      `json:"uses,omitempty"`     // users and critics: the project's equipment they work through
	Model       string        `json:"model,omitempty"`
	Source      string        `json:"source"` // house, project, agent
	Path        string        `json:"path"`
	Body        string        `json:"-"`
}

type frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Model       string `yaml:"model"`
	Administration *struct {
		Kind        string   `yaml:"kind"`
		Browser     *bool    `yaml:"browser"`
		Coins       string   `yaml:"coins"`
		SignsInAs   string   `yaml:"signs_in_as"`
		Sees        string   `yaml:"sees"`
		Knows       []string `yaml:"knows"`
		Constraints []string `yaml:"constraints"`
		Uses        []string `yaml:"uses"`
	} `yaml:"administration"`
}

// splitFrontmatter returns the YAML between the leading --- lines and the
// body after them.
func splitFrontmatter(b []byte) ([]byte, string, error) {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")) // a UTF-8 BOM
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	// "---" lines with trailing spaces still count.
	b = regexp.MustCompile(`(?m)^---[ \t]+$`).ReplaceAll(b, []byte("---"))
	if !bytes.HasPrefix(b, []byte("---\n")) {
		return nil, "", errors.New("no frontmatter (the file must start with ---)")
	}
	rest := b[4:]
	end := bytes.Index(rest, []byte("\n---\n"))
	if end < 0 {
		if bytes.HasSuffix(rest, []byte("\n---")) {
			end = len(rest) - 4
		} else {
			return nil, "", errors.New("frontmatter is never closed (no second ---)")
		}
	}
	body := ""
	if end+5 <= len(rest) {
		body = string(rest[end+5:])
	}
	return rest[:end], strings.TrimSpace(body), nil
}

func parseCoins(s string) (time.Duration, error) {
	if s == "" {
		return defaultCoins, nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		s = fmt.Sprintf("%dm", n)
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("coins %q: want a duration like 10m", s)
	}
	if d < time.Minute || d > 2*time.Hour {
		return 0, fmt.Errorf("coins %q: between 1m and 2h", s)
	}
	return d, nil
}

// parseProfessional reads one file. asAgent is for a project's existing
// agents: they need no administration: block and always join as critics.
func parseProfessional(b []byte, path, source string, asAgent bool) (*professional, error) {
	fm, body, err := splitFrontmatter(b)
	if err != nil {
		return nil, err
	}
	var m frontmatter
	if err := yaml.Unmarshal(fm, &m); err != nil {
		// An existing agent Claude Code loads may not be strict YAML (an
		// unquoted description with "SCOPE: ..." in it): read its top-level
		// lines as Claude Code does, each value as written.
		if !asAgent {
			return nil, fmt.Errorf("frontmatter: %v", err)
		}
		m = frontmatter{}
		for _, line := range strings.Split(string(fm), "\n") {
			k, v, ok := strings.Cut(line, ":")
			if !ok || strings.HasPrefix(line, " ") {
				continue
			}
			switch v = strings.TrimSpace(v); strings.TrimSpace(k) {
			case "name":
				m.Name = v
			case "description":
				m.Description = v
			case "model":
				m.Model = v
			}
		}
	}
	if m.Name == "" {
		m.Name = strings.TrimSuffix(filepath.Base(path), ".md")
	}
	if !nameRE.MatchString(m.Name) {
		return nil, fmt.Errorf("name %q: lowercase letters, digits and dashes", m.Name)
	}
	if strings.TrimSpace(m.Description) == "" {
		return nil, errors.New("no description (it's how a contract's cast is chosen)")
	}
	if body == "" {
		return nil, errors.New("no body (who they are and how they work)")
	}
	if m.Model != "" && !modelRE.MatchString(m.Model) {
		return nil, fmt.Errorf("model %q: a model name or alias like haiku, sonnet, opus", m.Model)
	}
	p := &professional{Name: m.Name, Description: strings.TrimSpace(m.Description), Model: m.Model,
		Source: source, Path: path, Body: body, Kind: "critic", Coins: defaultCoins}
	if c := m.Administration; c != nil && !asAgent {
		if c.Kind != "" {
			p.Kind = c.Kind
		}
		if p.Kind != "user" && p.Kind != "critic" {
			return nil, fmt.Errorf("administration.kind %q: user or critic (a command-line tool is a user's equipment: server = \"sandboxed\")", c.Kind)
		}
		p.Browser = p.Kind == "user"
		if c.Browser != nil {
			p.Browser = *c.Browser
		}
		for _, u := range c.Uses {
			if !nameRE.MatchString(u) {
				return nil, fmt.Errorf("administration.uses %q: the name of a piece of the project's equipment", u)
			}
		}
		p.Uses = c.Uses
		if p.Kind == "user" && !p.Browser && len(p.Uses) == 0 {
			return nil, errors.New("a user works through the browser (administration.browser: true) or the project's equipment (administration.uses)")
		}
		if p.Kind == "critic" && p.Browser {
			return nil, errors.New("a critic reads code; only a user has a browser")
		}
		if p.Coins, err = parseCoins(c.Coins); err != nil {
			return nil, err
		}
		p.SignsInAs = c.SignsInAs
		if p.Kind != "user" && p.SignsInAs != "" {
			return nil, errors.New("only a user signs in (signs_in_as)")
		}
		switch p.Sees = c.Sees; {
		case p.Sees != "" && p.Kind != "user":
			return nil, errors.New("only a user sees a screen (sees)")
		case p.Sees != "" && p.Sees != "screen" && p.Sees != "screen-reader":
			return nil, fmt.Errorf("administration.sees %q: screen (a person looking at it, the default) or screen-reader (a person using one)", p.Sees)
		}
		if p.Knows, err = cleanTags(c.Knows); err != nil {
			return nil, err
		}
		if len(p.Knows) == 0 {
			return nil, errors.New("no administration.knows tags (what they're trained for; it's how the Director casts them)")
		}
		for _, k := range c.Constraints {
			if k = strings.TrimSpace(k); k != "" {
				p.Constraints = append(p.Constraints, k)
			}
		}
		if p.SignsInAs != "" && !nameRE.MatchString(p.SignsInAs) {
			return nil, fmt.Errorf("signs_in_as %q: lowercase letters, digits and dashes", p.SignsInAs)
		}
	} else if !asAgent {
		return nil, errors.New("no administration: block (kind, coins, signs_in_as)")
	}
	p.CoinsMin = p.Coins.Minutes()
	return p, nil
}

// .administration/roster.toml. An agent the project already has joins as a
// critic, read-only, with its tags here so its own file stays untouched:
//
//	[critics.code-reviewer]
//	knows = ["code-review", "typescript"]
//	constraints = ["reads the diff only"]
type projectConfig struct {
	Critics map[string]struct {
		Knows       []string `toml:"knows"`
		Constraints []string `toml:"constraints"`
		// The agent's file, relative to the project, when it isn't in the
		// project's own .claude/agents (shared agents a level up, say).
		File string `toml:"file"`
		// The project's equipment it may run, to check what it reads: a
		// critic's commands are the only things it runs.
		Uses []string `toml:"uses"`
	} `toml:"critics"`
	Open []openRoom `toml:"open"`
	// Project names the project for the books, so its worktrees and copies
	// share one history and one set of dossiers.
	Project struct {
		Name string `toml:"name"`
	} `toml:"project"`
	// What professionals use to reach what they test, when it isn't the
	// web app or a command (docs/equipment.md).
	Equipment map[string]equipment `toml:"equipment"`
	// Documents of the project's own a browser professional works from
	// (house rules, viewport rules): copied into each user's desk, the only
	// files it can read.
	Handout struct {
		Files []string `toml:"files"`
	} `toml:"handout"`
	// The data a room can start from, besides the hook's default: the
	// project's own ways of getting data for local testing (a seed script,
	// a dump its tooling pulled). place --data NAME hands the name to the
	// setup hook as ADMINISTRATION_DATA; what it loads is the hook's.
	Data map[string]struct {
		About string `toml:"about"`
	} `toml:"data"`
	Setup struct {
		// Environment variables the setup and teardown hooks also need,
		// beyond PATH, HOME and the like (DOCKER_HOST, say). Never a token
		// the app could show a professional.
		PassEnv []string `toml:"pass_env"`
	} `toml:"setup"`
}

func loadProjectConfig(project string) projectConfig {
	var cfg projectConfig
	_, _ = toml.DecodeFile(filepath.Join(project, ".administration", "roster.toml"), &cfg)
	return cfg
}

var modelRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._\[\]-]{0,79}$`)

var tagRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

func cleanTags(in []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, t := range in {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] {
			continue
		}
		if !tagRE.MatchString(t) {
			return nil, fmt.Errorf("tag %q: lowercase letters, digits and dashes", t)
		}
		seen[t] = true
		out = append(out, t)
	}
	return out, nil
}

// An open room is work nobody on the roster is trained for yet: the
// Director's next hire.
type openRoom struct {
	Name  string   `json:"name" toml:"name"`
	Says  string   `json:"says" toml:"says"`
	Knows []string `json:"knows,omitempty" toml:"knows"`
}

type rosterView struct {
	Project       string          `json:"project"`
	Professionals []*professional `json:"professionals"`
	Open          []openRoom      `json:"open"`
	Problems      []string        `json:"problems,omitempty"`
}

// loadRoster reads the house, then the project's own hires (which may
// replace a house professional of the same name), then the project's
// existing agents named in .administration/roster.toml. A broken file is
// reported in Problems and left off, so one bad hire doesn't close the
// office.
func loadRoster(project string) (*rosterView, error) {
	v := &rosterView{Project: project, Professionals: []*professional{}}
	byName := map[string]*professional{}
	order := []string{}
	add := func(p *professional) {
		if prev, seen := byName[p.Name]; seen {
			if prev.Source != "house" && prev.Source != "machine" {
				// Two of the project's own with one name: keep the first, say so.
				v.Problems = append(v.Problems, fmt.Sprintf("%s: the name %q is already taken by %s; left off", rel(project, p.Path), p.Name, rel(project, prev.Path)))
				return
			}
		} else {
			order = append(order, p.Name)
		}
		byName[p.Name] = p
	}

	entries, _ := fs.ReadDir(house, "house")
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		b, _ := fs.ReadFile(house, "house/"+e.Name())
		p, err := parseProfessional(b, "house/"+e.Name(), "house", false)
		if err != nil {
			return nil, fmt.Errorf("house file %s: %v", e.Name(), err)
		}
		add(p)
	}
	var houseOpen struct {
		Open []openRoom `toml:"open"`
	}
	b, _ := fs.ReadFile(house, "house/open.toml")
	if _, err := toml.Decode(string(b), &houseOpen); err != nil {
		return nil, fmt.Errorf("house open.toml: %v", err)
	}
	v.Open = houseOpen.Open

	// The machine's own professionals: the ones not tied to a project (a
	// keyboard-only tester used everywhere, say). A project's hire of
	// the same name takes its place.
	mine, _ := filepath.Glob(filepath.Join(configDir(), "professionals", "*.md"))
	sort.Strings(mine)
	for _, f := range mine {
		b, err := os.ReadFile(f)
		if err == nil {
			var p *professional
			if p, err = parseProfessional(b, f, "machine", false); err == nil {
				add(p)
				continue
			}
		}
		v.Problems = append(v.Problems, fmt.Sprintf("%s: %v", f, err))
	}

	cont := filepath.Join(project, ".administration")
	own, _ := filepath.Glob(filepath.Join(cont, "professionals", "*.md"))
	sort.Strings(own)
	for _, f := range own {
		b, err := os.ReadFile(f)
		if err == nil {
			var p *professional
			if p, err = parseProfessional(b, f, "project", false); err == nil {
				add(p)
				continue
			}
		}
		v.Problems = append(v.Problems, fmt.Sprintf("%s: %v", rel(project, f), err))
	}

	var cfg projectConfig
	cfgPath := filepath.Join(cont, "roster.toml")
	if _, err := os.Stat(cfgPath); err == nil {
		if _, err := toml.DecodeFile(cfgPath, &cfg); err != nil {
			v.Problems = append(v.Problems, fmt.Sprintf("%s: %v", rel(project, cfgPath), err))
		}
	}
	critics := make([]string, 0, len(cfg.Critics))
	for name := range cfg.Critics {
		critics = append(critics, name)
	}
	sort.Strings(critics)
	for _, name := range critics {
		if !nameRE.MatchString(name) {
			v.Problems = append(v.Problems, fmt.Sprintf("critic %q in roster.toml: lowercase letters, digits and dashes", name))
			continue
		}
		f := filepath.Join(project, ".claude", "agents", name+".md")
		if file := cfg.Critics[name].File; file != "" {
			f = file
			if !filepath.IsAbs(f) {
				f = filepath.Join(project, f)
			}
		}
		b, err := os.ReadFile(f)
		if err == nil {
			var p *professional
			if p, err = parseProfessional(b, f, "agent", true); err == nil {
				if p.Name != name {
					err = fmt.Errorf("its name is %q, not %q", p.Name, name)
				} else if p.Knows, err = cleanTags(cfg.Critics[name].Knows); err == nil && len(p.Knows) == 0 {
					err = errors.New("no knows tags in roster.toml")
				} else if err == nil {
					if err = criticUses(cfg.Critics[name].Uses); err == nil {
						p.Constraints = append(p.Constraints, cfg.Critics[name].Constraints...)
						p.Uses = cfg.Critics[name].Uses
						add(p)
						continue
					}
				}
			}
		}
		v.Problems = append(v.Problems, fmt.Sprintf("critic %s (%s): %v", name, rel(project, f), err))
	}
	v.Open = append(v.Open, cfg.Open...)
	// A room is open only while nobody holds it.
	open := v.Open[:0]
	for _, o := range v.Open {
		if _, held := byName[o.Name]; !held {
			open = append(open, o)
		}
	}
	v.Open = open
	for _, n := range order {
		v.Professionals = append(v.Professionals, byName[n])
	}
	return v, nil
}

func rel(base, p string) string {
	if r, err := filepath.Rel(base, p); err == nil {
		return r
	}
	return p
}

func cmdRoster(args []string) error {
	f, err := parseFlags(args, map[string]bool{"json": false, "project": true, "match": true})
	if err != nil {
		return err
	}
	if len(f.pos) > 0 {
		return usageError{"roster takes no arguments"}
	}
	if f.has("match") {
		return cmdMatch(f)
	}
	project, err := projectDir(f.get("project"))
	if err != nil {
		return err
	}
	v, err := loadRoster(project)
	if err != nil {
		return err
	}
	if f.has("json") {
		return printJSON(v)
	}
	fmt.Printf("The roster (%s)\n\n", project)
	for _, p := range v.Professionals {
		extra := ""
		if p.SignsInAs != "" {
			extra = ", signs in as " + p.SignsInAs
		}
		fmt.Printf("  %-26s %-6s %-7s %2.0f coins%s\n      knows: %s\n      %s\n", p.Name, p.Kind, p.Source, p.CoinsMin, extra, strings.Join(p.Knows, ", "), oneLine(p.Description, 100))
	}
	if len(v.Open) > 0 {
		fmt.Println("\nOpen rooms (nobody holds these yet; /hire trains one):")
		for _, o := range v.Open {
			if o.Says != "" {
				fmt.Printf("  %-26s %s\n", o.Name, o.Says)
			} else {
				fmt.Printf("  %s\n", o.Name)
			}
		}
	}
	for _, p := range v.Problems {
		fmt.Printf("\nleft off the roster: %s\n", p)
	}
	return nil
}

func cmdCheck(args []string) error {
	if len(args) == 0 {
		return usageError{"check FILE..."}
	}
	bad := 0
	for _, f := range args {
		b, err := os.ReadFile(f)
		if err == nil {
			_, err = parseProfessional(b, f, "project", false)
		}
		if err != nil {
			fmt.Printf("%s: %v\n", f, err)
			bad++
		} else {
			fmt.Printf("%s: ok\n", f)
		}
	}
	if bad > 0 {
		return fmt.Errorf("%d of %d files need fixing", bad, len(args))
	}
	return nil
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		return s[:max-1] + "…"
	}
	return s
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// A match is the Director's first draft of a cast: everyone who knows at
// least one of the contract's tags, best first, and the tags nobody knows.
type match struct {
	Tags      []string    `json:"tags"`
	Cast      []matchItem `json:"cast"`
	Uncovered []string    `json:"uncovered"`
	Open      []openRoom  `json:"open_rooms_for_uncovered"`
}

type matchItem struct {
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`
	Matches []string `json:"matches"`
}

func cmdMatch(f *flags) error {
	tags, err := cleanTags(strings.Split(f.get("match"), ","))
	if err != nil {
		return usageError{err.Error()}
	}
	if len(tags) == 0 {
		return usageError{"--match needs tags: --match mobile,forms"}
	}
	project, err := projectDir(f.get("project"))
	if err != nil {
		return err
	}
	v, err := loadRoster(project)
	if err != nil {
		return err
	}
	m := match{Tags: tags, Cast: []matchItem{}, Uncovered: []string{}, Open: []openRoom{}}
	covered := map[string]bool{}
	for _, p := range v.Professionals {
		knows := map[string]bool{}
		for _, k := range p.Knows {
			knows[k] = true
		}
		var hits []string
		for _, t := range tags {
			if knows[t] {
				hits = append(hits, t)
				covered[t] = true
			}
		}
		if len(hits) > 0 {
			m.Cast = append(m.Cast, matchItem{Name: p.Name, Kind: p.Kind, Matches: hits})
		}
	}
	sort.SliceStable(m.Cast, func(i, j int) bool { return len(m.Cast[i].Matches) > len(m.Cast[j].Matches) })
	for _, t := range tags {
		if !covered[t] {
			m.Uncovered = append(m.Uncovered, t)
		}
	}
	for _, o := range v.Open {
		for _, k := range o.Knows {
			if slices.Contains(m.Uncovered, k) {
				m.Open = append(m.Open, o)
				break
			}
		}
	}
	if f.has("json") {
		return printJSON(m)
	}
	if len(m.Cast) == 0 {
		fmt.Println("Nobody on the roster is trained for this.")
	}
	for _, c := range m.Cast {
		fmt.Printf("  %-26s %-6s knows %s\n", c.Name, c.Kind, strings.Join(c.Matches, ", "))
	}
	if len(m.Uncovered) > 0 {
		fmt.Printf("\nNobody knows: %s. The Director can train someone (/hire).\n", strings.Join(m.Uncovered, ", "))
		for _, o := range m.Open {
			fmt.Printf("  open room %s: %s\n", o.Name, o.Says)
		}
	}
	return nil
}

// projectKey is what the books file a project under: its name from
// roster.toml, or its path.
func projectKey(project string) string {
	if n := loadProjectConfig(project).Project.Name; n != "" && nameRE.MatchString(n) {
		return n
	}
	return project
}

// A sandboxed equipment's commands are names or paths, one program each. Never a
// shell, an interpreter or anything that runs another command for it: any of
// those in the list would make the list meaningless (the sandbox would still
// hold, but the list is the first wall). Nothing that reaches the network
// or changes privileges either.
var refusedCommands = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "fish": true, "dash": true, "ksh": true, "csh": true, "tcsh": true,
	"python": true, "python3": true, "python2": true, "node": true, "nodejs": true, "deno": true, "bun": true,
	"perl": true, "ruby": true, "php": true, "lua": true, "osascript": true, "pwsh": true, "powershell": true,
	"env": true, "xargs": true, "exec": true, "eval": true, "nohup": true, "setsid": true, "timeout": true,
	"time": true, "nice": true, "watch": true, "sudo": true, "su": true, "doas": true, "chroot": true,
	"curl": true, "wget": true, "nc": true, "ncat": true, "socat": true, "ssh": true, "scp": true, "rsync": true,
	"ftp": true, "telnet": true, "git": true, "gh": true, "npm": true, "npx": true, "pnpm": true, "yarn": true,
	"pip": true, "pip3": true, "uv": true, "uvx": true, "cargo": true, "go": true, "make": true, "just": true,
	"docker": true, "podman": true, "kubectl": true, "open": true, "xdg-open": true, "crontinental": true,
	"administration": true, "claude": true, "find": true, "awk": true, "gawk": true, "sed": true, "vim": true,
	"vi": true, "emacs": true, "less": true, "more": true, "man": true, "tar": true, "zip": true, "unzip": true,
	"chmod": true, "chown": true, "ln": true, "rm": true, "mv": true, "cp": true, "dd": true, "tee": true,
	// Things that run other programs, or have a flag or hook that does.
	"busybox": true, "toybox": true, "stdbuf": true, "ionice": true, "chrt": true, "taskset": true, "flock": true,
	"unshare": true, "nsenter": true, "setpriv": true, "prlimit": true, "systemd-run": true, "dbus-run-session": true,
	"dbus-send": true, "busctl": true, "systemctl": true, "ssh-agent": true, "script": true, "expect": true,
	"unbuffer": true, "tmux": true, "screen": true, "parallel": true, "fd": true, "rg": true, "sort": true,
	"split": true, "gdb": true, "lldb": true, "strace": true, "ltrace": true, "valgrind": true, "nvim": true,
	"ed": true, "ex": true, "nano": true, "sqlite": true, "psql": true, "mysql": true, "tclsh": true, "wish": true,
	"java": true, "jshell": true, "Rscript": true, "R": true, "julia": true, "guile": true, "gjs": true, "qjs": true,
	"tsx": true, "ts-node": true, "corepack": true, "pandoc": true, "gs": true, "pytest": true, "tox": true,
	"install": true, "cpio": true, "pax": true, "7z": true, "7za": true, "bsdtar": true, "hyprctl": true,
	"osascript-": true, "launchctl": true, "at": true, "batch": true, "crontab": true, "pkexec": true,
}

func checkCommand(cmd string) error {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" || strings.ContainsAny(cmd, " \t;&|<>$`'\"(){}*?[]~\\\n") {
		return fmt.Errorf("command %q: one program, by name or path, nothing else", cmd)
	}
	for _, part := range strings.Split(cmd, "/") {
		if part == ".." {
			return fmt.Errorf("command %q: no .. in a command's path", cmd)
		}
	}
	// python3.12, perl5.40: the version doesn't change what it is.
	base := strings.TrimRight(filepath.Base(cmd), "0123456789.")
	if strings.HasPrefix(filepath.Base(cmd), "ld-linux") || strings.HasPrefix(filepath.Base(cmd), "ld.so") {
		return fmt.Errorf("command %q: a loader runs any program", cmd)
	}
	if refusedCommands[base] || refusedCommands[filepath.Base(cmd)] {
		return fmt.Errorf("command %q: shells, interpreters, network tools and anything that runs other commands can't be listed", cmd)
	}
	return nil
}

// equipment is a piece of the project's equipment: what a professional
// works through when it isn't the web app, started fresh for each room by
// the project's own hooks. Its server is the pinned browser server pointed
// at something (playwright), a shell allowed only its commands inside the
// OS sandbox (sandboxed: a CLI built into the room), the house's command
// server for programs that need to reach out of it (commands), the house's
// screen in front of the project's driver for a phone, a VM or a window
// (screen), or the project's own tool server. The person declares it; the Director can only
// use what's declared.
type equipment struct {
	About    string   `toml:"about"`
	Setup    string   `toml:"setup"`
	Teardown string   `toml:"teardown"`
	Server   string   `toml:"server"` // "playwright", "sandboxed", "commands", "screen", or a path in the project
	Driver   string   `toml:"driver"` // for server = "screen": the project's program that drives the device
	Keys     []string `toml:"keys"`   // for server = "screen": the keys its driver knows
	Args     []string `toml:"args"`
	Tools    []string `toml:"tools"`
	Commands []string `toml:"commands"` // for server = "sandboxed" or "commands"
	ShowArgs []string `toml:"show_args"`
	// Its setup reads what the app's room has (its database, its files): only
	// a user with the browser gets a room with the app in it.
	NeedsApp bool `toml:"needs_app"`
}

// notFor says why a professional of this kind can't work through eq, or "".
func (eq *equipment) notFor(kind string, browser bool) string {
	if kind == "critic" && (eq.Server == "playwright" || eq.Server == "sandboxed" || eq.Server == "screen") {
		return "a critic runs the project's commands equipment; a window, a screen or a sandboxed tool is a user's"
	}
	if eq.NeedsApp && kind == "critic" {
		return "it needs the app's room (needs_app), and a critic's room has no app: give the check to a user, or have the critic run the code that writes the rows"
	}
	if eq.NeedsApp && !browser {
		return "it needs the app's room (needs_app), and a user without the browser gets no app in its room"
	}
	return ""
}

// checkEquipment says what's wrong with the project's equipment called name.
func checkEquipment(project, name string) (*equipment, error) {
	if name == "browser" {
		return nil, errors.New(`"browser" is the web app's own browser: set administration.browser, not uses`)
	}
	eq, ok := loadProjectConfig(project).Equipment[name]
	if !ok {
		return nil, fmt.Errorf("no equipment called %q in .administration/roster.toml", name)
	}
	if strings.TrimSpace(eq.About) == "" {
		return nil, fmt.Errorf("equipment %s: no about (what it reaches and how to use it)", name)
	}
	for _, h := range []string{eq.Setup, eq.Teardown} {
		if h == "" {
			continue
		}
		if _, err := projectProgram(project, h); err != nil {
			return nil, fmt.Errorf("equipment %s: %v", name, err)
		}
	}
	switch eq.Server {
	case "playwright":
		if len(eq.Tools) == 0 {
			return nil, fmt.Errorf("equipment %s: list the tools it may use (tools)", name)
		}
		for _, t := range eq.Tools {
			if slices.Contains(deniedBrowserTools, t) || !slices.Contains(browserTools, t) {
				return nil, fmt.Errorf("equipment %s: %q isn't one of the browser tools a professional may have", name, t)
			}
		}
	case "sandboxed":
		// Run by name, or by a path inside the room's app/ (what the setup
		// built there), in the OS sandbox: one program each, never a shell.
		if len(eq.Commands) == 0 {
			return nil, fmt.Errorf("equipment %s: server = \"sandboxed\" needs commands", name)
		}
		for _, c := range eq.Commands {
			if err := checkCommand(c); err != nil {
				return nil, fmt.Errorf("equipment %s: %v", name, err)
			}
		}
	case "commands":
		if len(eq.Commands) == 0 {
			return nil, fmt.Errorf("equipment %s: server = \"commands\" needs commands", name)
		}
		for _, c := range eq.Commands {
			if _, err := projectProgram(project, c); err != nil {
				return nil, fmt.Errorf("equipment %s: %v", name, err)
			}
		}
	case "screen":
		// The house's screen: the professional gets its tools, the driver
		// is the project's.
		if eq.Driver == "" {
			return nil, fmt.Errorf("equipment %s: server = \"screen\" needs a driver (a program in the project)", name)
		}
		if _, err := projectProgram(project, eq.Driver); err != nil {
			return nil, fmt.Errorf("equipment %s: %v", name, err)
		}
		if len(eq.Tools) > 0 {
			return nil, fmt.Errorf("equipment %s: a screen's tools are the house's; leave tools out", name)
		}
		for _, k := range eq.Keys {
			if !toolNameRE.MatchString(k) {
				return nil, fmt.Errorf("equipment %s: key %q: letters, digits, _ and -", name, k)
			}
		}
	case "":
		return nil, fmt.Errorf("equipment %s: no server (playwright, sandboxed, commands, screen, or a program in the project)", name)
	default:
		if _, err := projectProgram(project, eq.Server); err != nil {
			return nil, fmt.Errorf("equipment %s: %v", name, err)
		}
		if len(eq.Tools) == 0 {
			return nil, fmt.Errorf("equipment %s: list the tools it may use (tools)", name)
		}
	}
	for _, t := range eq.Tools {
		if !toolNameRE.MatchString(t) {
			return nil, fmt.Errorf("equipment %s: tool %q: letters, digits, _ and -", name, t)
		}
	}
	return &eq, nil
}

var toolNameRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// projectProgram is an executable the project declared, by a path inside
// the project: one program, no arguments, no way out of the folder.
func projectProgram(project, rel string) (string, error) {
	if rel == "" || strings.ContainsAny(rel, " \t;&|<>$`'\"(){}*?[]~\\\n") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%q: a program's path inside the project, nothing else", rel)
	}
	abs := filepath.Join(project, rel)
	if !within(abs, project) {
		return "", fmt.Errorf("%q is outside the project", rel)
	}
	st, err := os.Stat(abs)
	if err != nil || st.IsDir() {
		return "", fmt.Errorf("%s isn't an executable file", rel)
	}
	if st.Mode()&0o111 == 0 {
		// Declared in the person's own roster, so running it is what they
		// asked for; a session that wrote it often can't chmod it. Only a
		// file that really is in the project (not a link out of it).
		real, err := filepath.EvalSymlinks(abs)
		proj, perr := filepath.EvalSymlinks(project)
		if err != nil || perr != nil || !within(real, proj) || !st.Mode().IsRegular() || os.Chmod(real, st.Mode()|0o100) != nil {
			return "", fmt.Errorf("%s isn't an executable file", rel)
		}
	}
	return abs, nil
}

const handoutLimit = 256 << 10

// copyHandout puts the project's handout into a user's desk and returns the
// names it copied. Each file is inside the project, a regular file, and no
// secret; together they stay under handoutLimit.
func copyHandout(project, desk string) ([]string, error) {
	var names []string
	total := int64(0)
	for _, rel := range loadProjectConfig(project).Handout.Files {
		src := filepath.Join(project, rel)
		if filepath.IsAbs(rel) || !within(src, project) {
			return nil, fmt.Errorf("handout %q: a file inside the project", rel)
		}
		for _, g := range secretGlobs {
			if ok, _ := filepath.Match(g, filepath.Base(rel)); ok {
				return nil, fmt.Errorf("handout %q looks like a secret; it never goes in a room", rel)
			}
		}
		st, err := os.Lstat(src)
		if err != nil || !st.Mode().IsRegular() {
			return nil, fmt.Errorf("handout %q: not a regular file in the project", rel)
		}
		if total += st.Size(); total > handoutLimit {
			return nil, fmt.Errorf("the handout is over %d KB; hand out less", handoutLimit>>10)
		}
		b, err := os.ReadFile(src)
		if err != nil {
			return nil, err
		}
		dst := filepath.Join(desk, "handout", filepath.Base(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(dst, b, 0o600); err != nil {
			return nil, err
		}
		names = append(names, filepath.Base(rel))
	}
	return names, nil
}

// criticUses checks the equipment names a critic's roster entry gives it.
func criticUses(uses []string) error {
	for _, u := range uses {
		if !nameRE.MatchString(u) {
			return fmt.Errorf("uses %q: the name of a piece of the project's equipment", u)
		}
	}
	return nil
}
