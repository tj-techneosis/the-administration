package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// Hooks are how the Administration ties into the rest of someone's work: an
// executable named after an event, in the project's .administration/hooks/ or
// in the machine's config (~/.config/administration/hooks/), hears the event
// as JSON on stdin and in ADMINISTRATION_* variables. Post the summary to a
// chat, open issues from confirmed findings, book a follow-up in the
// Crontinental: that's the hook's business, not the core's.
//
// A hook runs as the person, like the setup hook, gets hookLimit, and is
// stopped with its process group after that. Its exit code is ignored, its
// output goes to the books' hooks.log, and nothing about it changes an
// outcome.
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
		paths = append(paths, filepath.Join(project, ".administration", "hooks", event))
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

func runHook(path, project, event string, body []byte, payload map[string]any, logf *os.File) {
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
		return
	}
	if err := cmd.Start(); err != nil {
		if logf != nil {
			fmt.Fprintf(logf, "  could not start: %v\n", err)
		}
		return
	}
	go func() { _, _ = stdin.Write(body); stdin.Close() }()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
		if logf != nil {
			fmt.Fprintf(logf, "  took longer than %s and was stopped\n", hookLimit)
		}
	}
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
