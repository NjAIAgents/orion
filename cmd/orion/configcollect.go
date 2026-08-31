package main

// `orion config collect`: read or set the switches that govern how work lands.
//
// A sibling of `config limits` rather than an extension of it. Those are
// circuit breakers -- numbers that bound an agent -- and these are booleans
// that choose a landing strategy. Setting `batch_integration` through a
// command called "limits" would make the command's own name inaccurate, and
// the reason `config limits` exists at all (OR-198) is that a value which
// governs unattended behaviour has to be discoverable and stated plainly.
//
// It exists because otherwise these are unreachable. `config limits` walks
// config.Limits{} and only its int fields, so no boolean in config.Collect has
// ever had a command: auto_rebase has been settable only by hand-editing
// orion.json since it shipped, and batch_integration arrived the same way.
// Hand-editing is exactly what these commands exist to replace.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/orion-sdlc/orion/internal/adopt"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/ui"
)

// collectSwitch is one settable field, with the sentence an operator needs to
// decide. The prose is here rather than in the struct tag because what these
// mean is a paragraph, and a paragraph does not belong in a tag.
type collectSwitch struct {
	name string
	what string
	// risk is what turning it ON changes about unattended behaviour. Stated
	// for every switch, because a toggle whose consequences are unstated is
	// one people flip to see what happens.
	risk string
}

var collectSwitches = []collectSwitch{
	{
		name: "auto_rebase",
		what: "replay a branch that is behind its base and force-push it with a lease",
		risk: "the only automatic rewrite of a branch Orion performs. Safe to leave on: " +
			"git has already said the merge is clean, so the rebase has one possible result. " +
			"Turn it OFF for a repository you do not own -- a force-push to somebody " +
			"else's branch is theirs to authorise.",
	},
	{
		name: "batch_integration",
		what: "land the ready branches as one set: merge into an ephemeral ref, test it once",
		risk: "changes what merges and in what order. A mistake mis-merges or strands every " +
			"branch in flight at once rather than one at a time, which is why it is off by " +
			"default and why the first few batches are worth watching. " +
			"There is no batch size: a batch holds what finished, and no more can finish " +
			"than limits.max_concurrent_tickets allowed to run.",
	},
}

func collectSwitchNames() []string {
	out := make([]string, 0, len(collectSwitches))
	for _, s := range collectSwitches {
		out = append(out, s.name)
	}
	sort.Strings(out)
	return out
}

func findCollectSwitch(name string) (collectSwitch, bool) {
	for _, s := range collectSwitches {
		if s.name == name {
			return s, true
		}
	}
	return collectSwitch{}, false
}

func collectValue(c config.Collect, name string) bool {
	switch name {
	case "auto_rebase":
		return c.AutoRebase
	case "batch_integration":
		return c.BatchIntegration
	}
	return false
}

func runConfigCollect(args []string) {
	if err := configCollect(rootOrCwd(), os.Stdout, positional(args)); err != nil {
		fmt.Fprintf(os.Stderr, "orion: %v\n", err)
		os.Exit(1)
	}
}

// configCollect shows every switch with its value and what it costs, or sets
// one. Same root discovery as configLimits, so both answer for the same
// project from the same directory.
func configCollect(dir string, out io.Writer, args []string) error {
	root, err := config.FindRoot(dir)
	if err != nil {
		return fmt.Errorf("not inside an Orion project (no orion.json above %s).\n"+
			"  Run orion init there first", dir)
	}
	path := filepath.Join(root, "orion.json")

	switch len(args) {
	case 0:
		return showCollect(out, path)
	case 2:
		return setCollect(out, path, args[0], args[1])
	default:
		return fmt.Errorf("usage: orion config collect [KEY true|false]")
	}
}

func showCollect(out io.Writer, path string) error {
	cfg := config.Load(filepath.Dir(path))
	set := map[string]bool{}
	if b, err := os.ReadFile(path); err == nil {
		var raw struct {
			Collect map[string]any `json:"collect"`
		}
		if json.Unmarshal(b, &raw) == nil {
			for k := range raw.Collect {
				set[k] = true
			}
		}
	}
	fmt.Fprintf(out, "%s\n", ui.Heading(out, "how work lands"))
	for _, s := range collectSwitches {
		src := "default; not set in orion.json"
		if set[s.name] {
			src = "orion.json"
		}
		fmt.Fprintf(out, "  %-20s %-6v from %s\n", s.name, collectValue(cfg.Collect, s.name), src)
		fmt.Fprintf(out, "      %s\n", ui.Dim(out, s.what))
	}
	return nil
}

// addCollectBlock inserts a "collect" object carrying one switch.
//
// Through encoding/json rather than string surgery, so the result is valid by
// construction. It reformats the file, which is a real cost -- comments in a
// JSONC-style config would be lost -- so it is used ONLY when no block exists
// and adopt's in-place patch has nothing to patch.
func addCollectBlock(src, name string, v bool) (string, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(src), &doc); err != nil {
		return "", fmt.Errorf("orion.json is not valid JSON, so nothing was written: %w", err)
	}
	block, err := json.Marshal(map[string]bool{name: v})
	if err != nil {
		return "", err
	}
	doc["collect"] = block
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out) + "\n", nil
}

func setCollect(out io.Writer, path, name, value string) error {
	s, ok := findCollectSwitch(name)
	if !ok {
		return fmt.Errorf("%q is not a collect switch.\n  Known: %s",
			name, strings.Join(collectSwitchNames(), ", "))
	}
	v, err := strconv.ParseBool(value)
	if err != nil {
		return fmt.Errorf("%q is not true or false", value)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	next, changed, err := adopt.SetOrAddBlockField(string(b), "collect", name, strconv.FormatBool(v))
	if err != nil {
		// No "collect" block yet, which is every config written before this
		// existed. Telling the operator to add one by hand would be this
		// command instructing them to do the exact thing it exists to
		// replace, so it writes the block itself.
		next, err = addCollectBlock(string(b), name, v)
		if err != nil {
			return err
		}
		changed = true
	}
	if !json.Valid([]byte(next)) {
		return fmt.Errorf("patching %s would not leave valid JSON; nothing was written", path)
	}
	if changed {
		if err := os.WriteFile(path, []byte(next), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", path, err)
		}
		ui.Ok(out, "set", "collect.%s = %v in %s", name, v, path)
	} else {
		ui.Ok(out, "set", "collect.%s was already %v in %s", name, v, path)
	}

	// What it costs, said on the way IN rather than only in documentation.
	// These change unattended behaviour, and the moment someone turns one on
	// is the moment the consequence is worth reading.
	if v {
		fmt.Fprintf(out, "  %s\n", ui.Dim(out, s.risk))
	}
	// The same warning `config limits` gives, for the same reason: the value
	// is read at startup and held for the life of the process, so editing it
	// under a live watcher and waiting for a change that never comes is the
	// obvious way to conclude the setting does not work.
	fmt.Fprintf(out, "  %s\n", ui.Dim(out,
		"a watcher already running keeps the value it started with; restart it"))
	return nil
}
