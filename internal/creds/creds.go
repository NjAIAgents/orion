// Package creds resolves Orion's credentials from the environment or from
// Orion's own config file.
//
// Why a file at all, when environment variables already worked: a shell
// profile is read by INTERACTIVE shells only. Not by cron, not by launchd,
// not by a GUI-launched app. So credentials that work perfectly in a terminal
// vanish the moment `orion report --notify` runs on a schedule, and the
// failure looks like "Slack is not configured" rather than "your cron has a
// different environment". Orion reading its own file removes the class.
//
// Precedence is environment first, file second. That is the least surprising
// order: an explicitly exported variable is a deliberate override for one
// invocation, and a stored value should never win against it.
//
// The file holds secrets, so it is created 0600 at open time rather than
// chmod'ed afterwards. Writing world-readable and then narrowing leaves a
// window where anyone on the machine can read the token.
package creds

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Keys Orion understands. Kept as a list so `config` can iterate rather than
// hardcoding the same names in three places.
const (
	JiraURL    = "ORION_JIRA_URL"
	JiraEmail  = "ORION_JIRA_EMAIL"
	JiraToken  = "ORION_JIRA_TOKEN"
	SlackToken = "ORION_SLACK_TOKEN"
	Webhook    = "ORION_NOTIFY_WEBHOOK"
)

// Secret reports whether a key's value must never be displayed in full.
func Secret(key string) bool {
	switch key {
	case JiraToken, SlackToken, Webhook:
		return true
	}
	return false
}

// Known is every key, in the order a setup wizard should ask for them.
var Known = []string{JiraURL, JiraEmail, JiraToken, SlackToken, Webhook}

// Path is where Orion keeps its credentials.
func Path(home string) string { return filepath.Join(home, "config.env") }

// Get resolves one key: environment first, then the file.
func Get(home, key string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	m, _ := Load(home)
	return m[key]
}

// Source reports where a value came from, for a config listing that can
// explain itself rather than just showing a value.
func Source(home, key string) string {
	if strings.TrimSpace(os.Getenv(key)) != "" {
		return "environment"
	}
	if m, _ := Load(home); m[key] != "" {
		return "config file"
	}
	return ""
}

// Load reads the file. A missing file is not an error: it means nothing has
// been configured yet, which is a normal state rather than a fault.
func Load(home string) (map[string]string, error) {
	out := map[string]string{}
	f, err := os.Open(Path(home))
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return out, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Accept an `export ` prefix so a file someone pasted from their
		// shell profile still parses. Refusing it would be pedantry.
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = unquote(strings.TrimSpace(v))
	}
	return out, sc.Err()
}

func unquote(s string) string {
	if len(s) >= 2 {
		if s[0] == '\'' && s[len(s)-1] == '\'' {
			// Values are written with shell single-quote escaping so the file
			// can also be sourced by a shell if someone wants to. Reverse it
			// here, or a token containing a quote round-trips corrupted:
			// abc'def would come back as abc'\''def.
			return strings.ReplaceAll(s[1:len(s)-1], `'\''`, "'")
		}
		if s[0] == '"' && s[len(s)-1] == '"' {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// Save writes the given values, preserving any keys already in the file that
// are not being changed. Passing an empty value deletes a key, which is how
// a wizard lets someone clear a credential without editing the file.
func Save(home string, values map[string]string) error {
	existing, err := Load(home)
	if err != nil {
		return err
	}
	for k, v := range values {
		if strings.TrimSpace(v) == "" {
			delete(existing, k)
			continue
		}
		existing[k] = v
	}

	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}

	var b strings.Builder
	b.WriteString("# Orion credentials. Written by `orion config`.\n")
	b.WriteString("# Read by the orion binary directly, so this works under cron and\n")
	b.WriteString("# launchd where a shell profile would not. An exported environment\n")
	b.WriteString("# variable still wins over anything here.\n")
	b.WriteString("#\n")
	b.WriteString("# Contains secrets. Mode 0600. Do not commit this file.\n\n")

	keys := make([]string, 0, len(existing))
	for k := range existing {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s='%s'\n", k, strings.ReplaceAll(existing[k], "'", `'\''`))
	}

	// Write via a 0600 temp file and rename. Creating at 0600 rather than
	// chmod'ing afterwards closes the window where the token is readable by
	// anyone on the machine; the rename makes the replacement atomic, so a
	// crash mid-write cannot leave a truncated credentials file.
	tmp := Path(home) + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(b.String()); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, Path(home))
}

// Mask renders a secret safely: enough to recognise which credential it is,
// never enough to use it.
func Mask(v string) string {
	if v == "" {
		return ""
	}
	r := []rune(v)
	if len(r) <= 12 {
		return strings.Repeat("*", len(r))
	}
	return string(r[:6]) + strings.Repeat("*", 8) + string(r[len(r)-4:])
}

// PermsSupported reports whether Unix permission bits mean anything here.
//
// On Windows they do not. Go emulates them: Perm() returns 0666 whatever the
// real access control says, and Chmod only toggles the read-only attribute.
// Access is actually governed by NTFS ACLs, which the standard library cannot
// inspect. Reading the emulated bits there would produce a permanent
// "TOO OPEN, anyone can read your tokens" on every run, and a warning that is
// always wrong is worse than no warning: it teaches people to ignore the real
// ones.
func PermsSupported() bool { return runtime.GOOS != "windows" }

// CheckPerms reports whether the file is readable by anyone but its owner.
// Worth surfacing: a 0644 credentials file is a quiet, durable mistake.
//
// On a platform where the bits are meaningless it reports ok, because it
// genuinely cannot tell. Callers should use PermsSupported to say so rather
// than implying the file was verified.
func CheckPerms(home string) (bool, os.FileMode, error) {
	fi, err := os.Stat(Path(home))
	if err != nil {
		return true, 0, err
	}
	if !PermsSupported() {
		return true, fi.Mode().Perm(), nil
	}
	mode := fi.Mode().Perm()
	return mode&0o077 == 0, mode, nil
}

// Tighten fixes an over-permissive file.
func Tighten(home string) error { return os.Chmod(Path(home), 0o600) }
