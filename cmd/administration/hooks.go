package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Hooks are how the Administration ties into the rest of someone's work: an
// executable named after an event, in the project's house (hooks/) or
// in the machine's config (~/.config/administration/hooks/), hears the event
// as JSON on stdin and in ADMINISTRATION_* variables. Post the summary to a
// chat, open issues from confirmed findings, book a follow-up in the
// Crontinental: that's the hook's business, not the core's.
//
// A hook runs as the person, like the setup hook, gets hookLimit, and is
// stopped with its process group after that. Its exit code is ignored, its
// output goes to the books' hooks.log, and nothing about it changes an
// outcome. The watch hook is the exception: it opens the watch panes in
// place of the house's, so when it fails place says so and prints the
// watch line instead.
const hookLimit = 30 * time.Second

var hookEvents = map[string]bool{"placed": true, "closed": true, "hired": true, "director": true}

func configDir() string {
	if d := os.Getenv("ADMINISTRATION_CONFIG"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "administration")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "administration")
}

// fireHooks runs the event's hooks: the project's first, then the machine's.
func fireHooks(event, project string, payload map[string]any) {
	if !hookEvents[event] {
		return
	}
	payload["event"] = event
	payload["project"] = project
	body, _ := json.Marshal(payload)
	var paths []string
	if project != "" {
		paths = append(paths, filepath.Join(houseDir(project), "hooks", event))
	}
	paths = append(paths, filepath.Join(configDir(), "hooks", event))
	books, _ := booksDir()
	for _, path := range paths {
		st, err := os.Stat(path)
		if err != nil || st.IsDir() {
			continue
		}
		logf, _ := os.OpenFile(filepath.Join(books, "hooks.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if logf != nil {
			fmt.Fprintf(logf, "%s %s %s\n", time.Now().Format(time.DateTime), event, path)
		}
		if st.Mode()&0o111 == 0 {
			if logf != nil {
				fmt.Fprintf(logf, "  not executable; skipped\n")
				logf.Close()
			}
			continue
		}
		runHook(path, project, event, body, payload, logf)
		if logf != nil {
			logf.Close()
		}
	}
}

// runHook runs one hook and says whether it finished and exited 0, which
// only the watch hook's caller looks at.
func runHook(path, project, event string, body []byte, payload map[string]any, logf *os.File) error {
	ctx, cancel := context.WithTimeout(context.Background(), hookLimit)
	defer cancel()
	cmd := exec.Command(path)
	if project != "" {
		cmd.Dir = project
	}
	env := append(os.Environ(), "ADMINISTRATION_EVENT="+event, "ADMINISTRATION_PROJECT="+project)
	for _, k := range []string{"contract", "summary", "name", "run"} {
		if v, ok := payload[k].(string); ok {
			env = append(env, "ADMINISTRATION_"+map[string]string{"contract": "CONTRACT", "summary": "SUMMARY", "name": "NAME", "run": "RUN"}[k]+"="+v)
		}
	}
	cmd.Env = env
	if logf != nil {
		cmd.Stdout, cmd.Stderr = logf, logf
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		if logf != nil {
			fmt.Fprintf(logf, "  could not start: %v\n", err)
		}
		return err
	}
	go func() { _, _ = stdin.Write(body); stdin.Close() }()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
		if logf != nil {
			fmt.Fprintf(logf, "  took longer than %s and was stopped\n", hookLimit)
		}
		return fmt.Errorf("took longer than %s", hookLimit)
	}
}

// watchHook is the executable that opens the watch panes in place of the
// built-in Herdr and tmux ones, or "" for none. Unlike the other events only
// one runs, since two would open everything twice, and the machine's comes
// first: where panes appear is the person's choice, not the project's.
func watchHook(project string) string {
	paths := []string{filepath.Join(configDir(), "hooks", "watch")}
	if project != "" {
		paths = append(paths, filepath.Join(houseDir(project), "hooks", "watch"))
	}
	for _, path := range paths {
		st, err := os.Stat(path)
		if err != nil || st.IsDir() {
			continue
		}
		if st.Mode()&0o111 != 0 {
			return path
		}
		// Once per run: place asks twice (whether to watch, then to open).
		if logf := hooksLog(); logf != nil && !skipLogged[path] {
			skipLogged[path] = true
			fmt.Fprintf(logf, "%s watch %s\n  not executable; skipped\n", time.Now().Format(time.DateTime), path)
			logf.Close()
		}
	}
	return ""
}

var skipLogged = map[string]bool{}

// hooksLog opens the books' hooks.log to append to, or nil.
func hooksLog() *os.File {
	books, err := booksDir()
	if err != nil {
		return nil
	}
	f, _ := os.OpenFile(filepath.Join(books, "hooks.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	return f
}

// runWatchHook hands the hook the contract and, for each professional, the
// command that follows them and exits once they're back.
func runWatchHook(path, project, id, dir string, names []string, cmdFor func(string) string) error {
	pros := make([]map[string]string, 0, len(names))
	for _, n := range names {
		pros = append(pros, map[string]string{"name": n, "command": cmdFor(n)})
	}
	payload := map[string]any{"event": "watch", "project": project, "contract": id, "books": dir, "professionals": pros}
	body, _ := json.Marshal(payload)
	logf := hooksLog()
	if logf != nil {
		fmt.Fprintf(logf, "%s watch %s\n", time.Now().Format(time.DateTime), path)
		defer logf.Close()
	}
	err := runHook(path, project, "watch", body, payload, logf)
	// runHook already logged a timeout; an exit status is ours to log.
	if ee, ok := err.(*exec.ExitError); ok {
		err = fmt.Errorf("exited %d", ee.ExitCode())
		if logf != nil {
			fmt.Fprintf(logf, "  %v\n", err)
		}
	}
	if err != nil && strings.HasPrefix(err.Error(), "took longer") {
		err = fmt.Errorf("%v and was stopped, so some panes may be open already", err)
	}
	return err
}

// contractPayload is what contract hooks hear: the contract, each
// professional's status, and where the summary is.
func contractPayload(c *contract, dir string) map[string]any {
	st := map[string]status{}
	for _, p := range c.Professionals {
		st[p.Name] = readStatus(dir, p.Name)
	}
	p := map[string]any{"contract": c.ID, "target": c.Target, "professionals": c.Professionals, "status": st, "books": dir}
	if _, err := os.Stat(filepath.Join(dir, "summary.md")); err == nil {
		p["summary"] = filepath.Join(dir, "summary.md")
	}
	return p
}
