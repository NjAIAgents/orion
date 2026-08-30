package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/orion-sdlc/orion/internal/adopt"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/watch"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// runConfigLimits is `orion config limits`: read every circuit breaker with
// its provenance, or write one of them, without opening an editor.
//
// A subcommand rather than a flag on `orion watch` (OR-198). A flag applies
// to one invocation and vanishes with it, so an unattended watcher restarted
// by anything other than that exact command line silently reverts to the
// file -- which makes a flag a fine one-off experiment and a poor way to set
// a value. And a subcommand beside `config agents` keeps one mechanism for
// configuration rather than a second style next to it.
//
// Generic over the whole limits block rather than special-cased to
// max_concurrent_tickets. Concurrency is the one being asked for because it
// is the one that changes spend, but a setter for exactly one key is how a
// second bespoke setter gets written next quarter.
func runConfigLimits(args []string) {
	if err := configLimits(workspace.Home(), rootOrCwd(), os.Stdout, positional(args)); err != nil {
		fmt.Fprintf(os.Stderr, "orion: %v\n", err)
		os.Exit(1)
	}
}

func configLimits(home, dir string, out io.Writer, args []string) error {
	root, err := config.FindRoot(dir)
	if err != nil {
		return fmt.Errorf("not inside an Orion project (no orion.json above %s).\n"+
			"  Run orion init there first", dir)
	}
	path, key := limitsFile(home, root)

	switch len(args) {
	case 0:
		return showLimits(home, out, path, key)
	case 2:
		return setLimit(home, out, path, key, args[0], args[1])
	default:
		return fmt.Errorf("usage: orion config limits [KEY VALUE]")
	}
}

// limitsFile resolves WHICH orion.json to read and write, and it is the
// registry's copy rather than the one underfoot whenever the project is
// registered.
//
// This matters more than it looks. The watcher resolves the cap with
// config.Load(e.Source) -- the USER'S WORKING COPY, not the sandbox clone a
// run executes in -- so a setter that wrote to whichever root the caller
// happened to be standing in could edit the clone, report success, and
// change nothing the watcher will ever read. Reader and writer have to name
// the same file. (Policy read from the sandbox clone, which bind.go
// describes, is a different path and easy to conflate with this one.)
//
// An unregistered project has no entry to disagree with, so the local root
// is both the only answer and the right one.
func limitsFile(home, root string) (path, key string) {
	key = config.Load(root).Tracker.ProjectKey
	if key != "" {
		if e, err := registry.Lookup(home, key); err == nil && e.Source != "" {
			return filepath.Join(e.Source, "orion.json"), key
		}
	}
	return filepath.Join(root, "orion.json"), key
}

func showLimits(home string, out io.Writer, path, key string) error {
	fmt.Fprintf(out, "file  %s\n", path)
	if key != "" {
		fmt.Fprintf(out, "key   %s\n", key)
	}
	fmt.Fprintln(out)

	set, err := limitsInFile(path)
	if err != nil {
		return err
	}
	cfg := config.Load(filepath.Dir(path))
	for _, name := range limitNames() {
		src := "default; not set in orion.json"
		if _, ok := set[name]; ok {
			src = "orion.json"
		}
		fmt.Fprintf(out, "  %-26s %-6d from %s\n", name, limitValue(cfg.Limits, name), src)
	}

	// The watcher's own number, from the watcher's own function rather than
	// re-derived here: the cap is the smallest across every project a watcher
	// spans, and a second implementation of that rule would eventually
	// disagree with the banner it is meant to explain.
	n, from := watch.Concurrency(home, projectScope(key))
	fmt.Fprintln(out)
	fmt.Fprintf(out, "%s\n", ui.Heading(out, "what a watcher would run"))
	fmt.Fprintf(out, "  %s\n", ui.Dim(out, fmt.Sprintf("at once   %d ticket(s) (%s)", n, from)))
	return nil
}

func setLimit(home string, out io.Writer, path, key, name, value string) error {
	if _, ok := limitField(name); !ok {
		return fmt.Errorf("%q is not a limit.\n  Known: %s", name, strings.Join(limitNames(), ", "))
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 {
		return fmt.Errorf("%q is not a whole number of zero or more", value)
	}
	// Refused, not clamped. ConcurrentTickets() clamps to the ceiling at read
	// time, so storing 40 would leave the file saying 40 while the watcher ran
	// 5 -- a file that disagrees with behaviour, the same class of problem as
	// the slot arithmetic nobody could see in OR-196. Saying no costs one
	// re-run and leaves nothing to misread.
	if name == "max_concurrent_tickets" && n > config.MaxConcurrentTicketsCeiling {
		return fmt.Errorf("limits.max_concurrent_tickets is capped at %d, so %d would be run as %d.\n"+
			"  Nothing was written. Ask for %d or less.",
			config.MaxConcurrentTicketsCeiling, n, config.MaxConcurrentTicketsCeiling,
			config.MaxConcurrentTicketsCeiling)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	next, changed, err := adopt.SetOrAddBlockField(string(b), "limits", name, strconv.Itoa(n))
	if err != nil {
		return fmt.Errorf("%s has no \"limits\" block to write into; add one and re-run", path)
	}
	if !json.Valid([]byte(next)) {
		return fmt.Errorf("patching %s would not leave valid JSON; nothing was written", path)
	}
	if changed {
		if err := os.WriteFile(path, []byte(next), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", path, err)
		}
		ui.Ok(out, "set", "limits.%s = %d in %s", name, n, path)
	} else {
		ui.Ok(out, "set", "limits.%s was already %d in %s", name, n, path)
	}
	if n == 0 {
		fmt.Fprintf(out, "  %s\n", ui.Dim(out, "zero restores the shipped default; it does not mean unlimited"))
	}

	if name == "max_concurrent_tickets" {
		eff, from := watch.Concurrency(home, projectScope(key))
		fmt.Fprintf(out, "  %s\n", ui.Dim(out, fmt.Sprintf("at once   %d ticket(s) (%s)", eff, from)))
	}
	// Said every time, because the alternative is someone editing the value
	// under a live watcher, waiting for a third agent that is never coming,
	// and concluding the setting does not work. The cap is read once at
	// startup and held for the life of the process.
	fmt.Fprintf(out, "  %s\n", ui.Dim(out, "a watcher already running keeps the value it started with; restart it"))
	return nil
}

// projectScope is what to hand watch.Concurrency: this project alone when it
// has a key, every registered project when it does not.
func projectScope(key string) []string {
	if key == "" {
		return nil
	}
	return []string{key}
}

// limitsInFile reports which limit keys the file states outright, as opposed
// to the ones config.Load fills in afterwards. Without this every limit would
// read as configured, which is precisely the confusion OR-198 is about: an
// operator told to change max_concurrent_tickets does not find it, because
// the value in force was never in the file.
//
// json.RawMessage rather than int because the block also carries
// "_comment_*" strings, and a comment must not make the whole read fail.
func limitsInFile(path string) (map[string]json.RawMessage, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var doc struct {
		Limits map[string]json.RawMessage `json:"limits"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", path, err)
	}
	if doc.Limits == nil {
		doc.Limits = map[string]json.RawMessage{}
	}
	return doc.Limits, nil
}

// limitNames lists the settable limits from config.Limits itself, so a limit
// added to the struct is settable the day it exists rather than the day
// somebody remembers this file.
func limitNames() []string {
	t := reflect.TypeOf(config.Limits{})
	out := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		if tag := jsonName(t.Field(i)); tag != "" {
			out = append(out, tag)
		}
	}
	sort.Strings(out)
	return out
}

func limitField(name string) (reflect.StructField, bool) {
	t := reflect.TypeOf(config.Limits{})
	for i := 0; i < t.NumField(); i++ {
		if jsonName(t.Field(i)) == name {
			return t.Field(i), true
		}
	}
	return reflect.StructField{}, false
}

func limitValue(l config.Limits, name string) int {
	f, ok := limitField(name)
	if !ok {
		return 0
	}
	return int(reflect.ValueOf(l).FieldByIndex(f.Index).Int())
}

func jsonName(f reflect.StructField) string {
	if f.Type.Kind() != reflect.Int {
		return ""
	}
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	if name == "-" {
		return ""
	}
	return name
}
