package main

import (
	"fmt"
	"os"
	"regexp"
	"path/filepath"
	"strings"
)

// A dossier is what a professional learned on earlier contracts in this
// project: routes that work, logins to use, known issues not to report
// again, dead ends to skip. The Director writes it and keeps it short; it
// goes into the professional's persona at place. It's read as notes, never
// as instructions, and it can't widen the wall: everything a dossier could
// ask for, the wall refuses anyway.
const dossierLimit = 6 << 10

func dossierPath(project, name string) string {
	return filepath.Join(houseDir(project), "dossiers", name+".md")
}

// readDossier is the dossier, cut at the limit (the Director's job is to stay
// under it; a long one is never allowed to crowd out the brief).
func readDossier(project, name string) string {
	if !nameRE.MatchString(name) {
		return ""
	}
	b, err := os.ReadFile(dossierPath(project, name))
	if err != nil {
		// A machine professional keeps its dossier with the machine's roster.
		if b, err = os.ReadFile(filepath.Join(configDir(), "dossiers", name+".md")); err != nil {
			return ""
		}
	}
	s := strings.TrimSpace(string(b))
	if problems := dossierLint(s); len(problems) > 0 {
		// Left out, not trimmed: a dossier that reads like a way out of the
		// room is a person's to look at, not a professional's to read.
		fmt.Fprintf(os.Stderr, "administration: %s's dossier was left out: %s\n", name, strings.Join(problems, "; "))
		return ""
	}
	if len(s) > dossierLimit {
		s = s[:dossierLimit] + "\n[cut at the dossier limit]"
	}
	return s
}

// Dossiers are notes, and findings that fed them may carry text from the
// app under test. dossierLint is a plain check, not a judgement: anything
// that reads like a way out of the room (a non-local address, a shell
// command, an order to ignore the rules) stops a Director run from being
// applied until a person looks.
var (
	lintURL     = regexp.MustCompile(`(?i)\b(?:https?|wss?|ftp)://([^/\s:)]+)`)
	lintCommand = regexp.MustCompile("(?i)(?:^|[`\\s(])(curl|wget|nc|ssh|scp|rm -rf|sudo|chmod|bash -c|sh -c|python3? -c|node -e|git push|npm publish)\\b")
	lintOrder   = regexp.MustCompile(`(?i)\b(ignore (?:the |all |any |previous |your )?(?:rules|instructions|brief)|disregard|you must now|new instructions|system prompt)\b`)
	lintSecret  = regexp.MustCompile(`(?i)(~/\.ssh|id_rsa|id_ed25519|\.aws/|\.supabase|\.npmrc|\.netrc|gopass|keychain)`)
)

func dossierLint(text string) []string {
	var out []string
	for _, m := range lintURL.FindAllStringSubmatch(text, -1) {
		if h := strings.ToLower(m[1]); h != "localhost" && h != "127.0.0.1" {
			out = append(out, fmt.Sprintf("names an address off this machine (%s)", m[0]))
		}
	}
	if m := lintCommand.FindStringSubmatch(text); m != nil {
		out = append(out, fmt.Sprintf("names a shell command (%s)", strings.TrimSpace(m[1])))
	}
	if m := lintOrder.FindString(text); m != "" {
		out = append(out, fmt.Sprintf("reads like an instruction to the professional (%q)", m))
	}
	if m := lintSecret.FindString(text); m != "" {
		out = append(out, fmt.Sprintf("names a secret's location (%s)", m))
	}
	return out
}
