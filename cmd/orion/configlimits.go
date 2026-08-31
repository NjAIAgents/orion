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

// confirmConcurrency states what a large number costs and asks for a yes.
//
// Every hazard here has been observed on this project rather than imagined,
// which is why they are worth reading rather than clicking past.
// confirmIn is where the answer is read. A variable so a test can supply one:
// reading os.Stdin directly made this unreachable from a test, which for a
// prompt that guards an unattended setting is the wrong thing to leave
// unverified.
var confirmIn io.Reader = os.Stdin

func confirmConcurrency(out io.Writer, n int) bool {
	fmt.Fprintf(out, "\n%s\n", ui.Heading(out, fmt.Sprintf("%d tickets at once", n)))
	for _, line := range []string{
		"conflicts grow with the SQUARE of this number, not with it: six pairs at four, " +
			"forty-five at ten. One pair in six collided on this project.",
		"every run holds a worktree off ONE shared clone, and git serialises on it.",
		"one rate limit is shared by all of them; the more that run, the sooner they wait.",
		"a budget checkpoint is only read between runs, so N runs already in flight " +
			"sail past it together.",
		"approvals do NOT parallelise: N tickets finishing means N approvals waiting " +
			"on one person.",
	} {
		fmt.Fprintf(out, "  %s %s\n", ui.Dim(out, "-"), ui.Dim(out, line))
	}
	fmt.Fprintf(out, "\nSet it to %d anyway? [y/N] ", n)
	var answer string
	_, _ = fmt.Fscanln(confirmIn, &answer)
	answer = strings.ToLower(strings.TrimSpace(answer))
	// Anything that is not an explicit yes is a no, including EOF. A prompt
	// that no one answered has not been agreed to, and defaulting to yes when
	// stdin is closed would let a script set a number nobody chose.
	return answer == "y" || answer == "yes"
}

func setLimit(home string, out io.Writer, path, key, name, value string) error {
	if _, ok := limitField(name); !ok {
		return fmt.Errorf("%q is not a limit.\n  Known: %s", name, strings.Join(limitNames(), ", "))
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 {
		return fmt.Errorf("%q is not a whole number of zero or more", value)
	}
	// Confirmed, not refused. This used to reject anything above a hard
	// ceiling of five, which meant Orion overruling a number the operator had
	// chosen for a machine it cannot measure. The hazards below are real, and
	// they scale with the machine, the repository and the rate limit -- so the
	// right move is to state them and let the person decide.
	if name == "max_concurrent_tickets" && n > config.ConcurrencyWarnAbove {
		if !confirmConcurrency(out, n) {
			return fmt.Errorf("not confirmed, so nothing was written; "+
				"%d is above the point (%d) where this asks",
				n, config.ConcurrencyWarnAbove)
		}
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
