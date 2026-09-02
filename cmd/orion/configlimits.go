package main

import (
	"encoding/json"
	"errors"
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
	// The qualified ones, listed in the same table rather than a section of
	// their own. They are circuit breakers by the same definition as the nine
	// above -- each bounds a loop that would otherwise run until it ran out of
	// money -- and a reader looking for "every bound" should find them all in
	// one place. Their block prefix is what says they live elsewhere in the
	// JSON; that is the whole reason the name is qualified.
	for _, name := range qualifiedNames() {
		q := qualifiedLimits[name]
		fields, err := blockInFile(path, q.block)
		if err != nil {
			return err
		}
		src := "default; not set in orion.json"
		if _, ok := fields[q.field]; ok {
			src = "orion.json"
		}
		fmt.Fprintf(out, "  %-26s %-6d from %s\n", name, q.value(cfg), src)
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
	_, plain := limitField(name)
	q, qualified := qualifiedLimits[name]
	if !plain && !qualified {
		return fmt.Errorf("%q is not a limit.\n  Known: %s", name, strings.Join(allLimitNames(), ", "))
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 {
		return fmt.Errorf("%q is not a whole number of zero or more", value)
	}
	if qualified {
		return setQualifiedLimit(out, path, name, q, n)
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

	// Read BEFORE the patch. What a person needs in order to judge a set they
	// just typed is what it was, not only what it now is -- "= 8" alone does
	// not say whether that was a change of one or of six.
	before := limitValue(config.Load(filepath.Dir(path)).Limits, name)

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
		ui.Ok(out, "set", "limits.%s = %d (was %d) in %s", name, n, before, path)
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

// qualifiedLimit is a circuit breaker that does NOT live in the limits block.
//
// A fix-round ceiling IS a circuit breaker: it bounds a loop that would
// otherwise run until it ran out of money, which is exactly what the nine keys
// in the limits block do, and this command's own heading calls them circuit
// breakers. So it belongs under this command rather than a second one beside
// it -- "one command for every bound" is the property worth having.
//
// The alternative was to MOVE these fields into the limits block, which reads
// tidier and is a breaking config change for every repository that has already
// set qa.max_rounds, in exchange for nothing but tidiness. A qualified name
// leaves the JSON exactly where it is; the block prefix in the name is what
// tells a reader where to look.
//
// The name is therefore block.field, matching the JSON path literally. A
// friendlier alias -- ci.fix_attempts for ci.max_fix_attempts -- would be a
// name that appears nowhere in the file it edits, which is the confusion this
// whole ticket is about.
type qualifiedLimit struct {
	block, field string
	// value is the EFFECTIVE number, default applied, read through the same
	// accessor the stage itself reads. Re-deriving it here is how a listing
	// starts disagreeing with the thing it claims to describe.
	value func(config.Config) int
	// hazards are what a large value costs, stated when one is asked for.
	hazards []string
}

var qualifiedLimits = map[string]qualifiedLimit{
	"qa.max_rounds": {
		block: "qa", field: "max_rounds",
		value: func(c config.Config) int { return c.QA.Rounds() },
		hazards: []string{
			"QA never blocks on its own authority, so this loop is the only way the " +
				"stage can stop a run -- and it would do it by spending.",
			"each round is a full findings-fix-reverify exchange between two agents.",
			"the cost lands on the implementer, which is the actor that dominates " +
				"per-ticket spend.",
		},
	},
	"qa.verdict_minutes": {
		block: "qa", field: "verdict_minutes",
		// The FLOOR, which is what is stored and what this sets. The effective
		// budget scales with the run being resumed, so a listing that showed
		// the scaled figure would print a number that is true of one run and
		// of no other.
		value: func(c config.Config) int { return c.QA.VerdictFloor() },
		hazards: []string{
			"this bounds the verdict RE-ASK, not the QA run: one question put to a " +
				"session that has already done the work.",
			"the budget SCALES with the run it resumes, so this is the floor and the " +
				"short case, not the ceiling -- raising it raises every re-ask.",
			"too LOW is the expensive direction here, and the reason this is settable: " +
				"a re-ask killed by the clock produces neither a verdict nor a fix " +
				"round, just an unverified branch and a person sent to read it (OR-248).",
		},
	},
	"qa.author_agents": {
		block: "qa", field: "author_agents",
		value: func(c config.Config) int { return c.QA.Authors() },
		hazards: []string{
			"this bounds AGENTS, which contend for one rate limit -- not processes, " +
				"which contend for CPU. qa.exec_procs is the other one.",
			"limits.max_concurrent_children is still the hard ceiling: supervisor.Fan " +
				"reads it directly, so a larger number here cannot widen the real fan.",
			"a wider fan is more agents writing at once, and every one of them is a " +
				"session that reads the ticket before it writes anything.",
		},
	},
	"qa.exec_procs": {
		block: "qa", field: "exec_procs",
		value: func(c config.Config) int { return c.QA.Procs() },
		hazards: []string{
			"this bounds PROCESSES on this machine, not agents: it is passed to the " +
				"runner's own flag (go test -p), not used to spawn anything here.",
			"too high contends for CPU, disk and any port the tests bind, and a suite " +
				"that fails only under load reads exactly like a flaky one.",
		},
	},
	"dba.max_rounds": {
		block: "dba", field: "max_rounds",
		value: func(c config.Config) int { return c.DBA.Rounds() },
		hazards: []string{
			"the database review never blocks on its own authority either, so this " +
				"loop is the only way that stage can stop a run -- and it would do it " +
				"by spending.",
			"each round is a full findings-fix-review exchange, and the review runs on " +
				"opus: a schema decision is inherited by everything written against it " +
				"afterwards, which is what buys the expensive model.",
			"this stage only runs on tickets that touch data, so raising it costs " +
				"nothing on the ones that do not -- and everything on the ones that do.",
		},
	},
	"ci.max_fix_attempts": {
		block: "ci", field: "max_fix_attempts",
		value: func(c config.Config) int { return c.CI.Attempts() },
		hazards: []string{
			"this is the OUTER bound only. An identical repeated failure already ends " +
				"the loop at once, so raising this buys attempts only for a run that " +
				"produces a DIFFERENT failure every round.",
			"each attempt is an agent run plus a full CI run, and the attempt count " +
				"survives a restart, so it bounds the problem rather than one invocation.",
			"the cost lands on the implementer, which is the actor that dominates " +
				"per-ticket spend.",
		},
	},
}

func qualifiedNames() []string {
	out := make([]string, 0, len(qualifiedLimits))
	for name := range qualifiedLimits {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// allLimitNames is what an unknown key is told about: every bound this command
// can set, plain and qualified together. Listing only the plain ones is the
// gap that made these two invisible in the first place -- `orion config limits
// max_rounds 3` answered "not a limit" and then named nine keys that did not
// include the thing being asked for.
func allLimitNames() []string { return append(limitNames(), qualifiedNames()...) }

// setQualifiedLimit writes one field of a block outside limits.
//
// Deliberately parallel to setLimit rather than merged with it: the two differ
// in which block they patch, whether the block may be missing, and what the
// closing advice is. Merging them would mean two conditionals inside every
// step of a function whose whole job is one write.
func setQualifiedLimit(out io.Writer, path, name string, q qualifiedLimit, n int) error {
	// Confirmed, not refused -- the same answer max_concurrent_tickets gives
	// since OR-246, and for the same reason. Refusing would be Orion
	// overruling a number the operator chose for a repository it cannot
	// measure from here: a project whose failures genuinely take four
	// exchanges to converge exists, and Orion cannot tell it apart from a
	// typo. The person typing the number can, so it is stated and asked.
	//
	// The hazard this guards is real and asymmetric: too high is the expensive
	// direction. A max_rounds of 40 in a hand-edited file is a very costly way
	// to discover that an agent cannot fix something.
	if n > config.FixRoundsWarnAbove {
		if !confirmFixRounds(out, name, n, q.hazards) {
			return fmt.Errorf("not confirmed, so nothing was written; "+
				"%d is above the point (%d) where this asks",
				n, config.FixRoundsWarnAbove)
		}
	}

	before := q.value(config.Load(filepath.Dir(path)))

	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	next, changed, err := adopt.SetOrAddBlockField(string(b), q.block, q.field, strconv.Itoa(n))
	if errors.Is(err, adopt.ErrNoBlock) {
		// The block is created rather than demanded. Answering "add a %q block
		// and re-run" would send the operator to hand-edit the file, which is
		// the exact thing this command exists so that nobody has to do.
		next, changed, err = addBlockField(string(b), q.block, q.field, strconv.Itoa(n))
	}
	if err != nil {
		return fmt.Errorf("patching %s: %w", path, err)
	}
	if !json.Valid([]byte(next)) {
		return fmt.Errorf("patching %s would not leave valid JSON; nothing was written", path)
	}
	if changed {
		if err := os.WriteFile(path, []byte(next), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", path, err)
		}
		ui.Ok(out, "set", "%s = %d (was %d) in %s", name, n, before, path)
	} else {
		ui.Ok(out, "set", "%s was already %d in %s", name, n, path)
	}
	if n == 0 {
		fmt.Fprintf(out, "  %s\n", ui.Dim(out, "zero restores the shipped default; it does not mean unlimited"))
	}
	// Not the watcher note the plain limits carry. These two are read from
	// config at the start of each stage, not once at watcher startup, so a run
	// already in flight keeps the old number and the next one picks this up.
	fmt.Fprintf(out, "  %s\n", ui.Dim(out, "a run already in flight keeps the value it started with; the next one uses this"))
	return nil
}

// confirmFixRounds states what a large fix-round ceiling costs and asks for a
// yes. Same shape as confirmConcurrency, reading from the same confirmIn so a
// test can answer it -- a prompt guarding an unattended setting is the wrong
// thing to leave unverified.
func confirmFixRounds(out io.Writer, name string, n int, hazards []string) bool {
	fmt.Fprintf(out, "\n%s\n", ui.Heading(out, fmt.Sprintf("%s = %d", name, n)))
	for _, line := range hazards {
		fmt.Fprintf(out, "  %s %s\n", ui.Dim(out, "-"), ui.Dim(out, line))
	}
	fmt.Fprintf(out, "\nSet it to %d anyway? [y/N] ", n)
	var answer string
	_, _ = fmt.Fscanln(confirmIn, &answer)
	answer = strings.ToLower(strings.TrimSpace(answer))
	// Anything that is not an explicit yes is a no, including EOF.
	return answer == "y" || answer == "yes"
}

// addBlockField appends a new top-level block holding one field, for a file
// that has never carried the block being set.
//
// A text insert rather than a JSON round trip, for the same reason
// SetOrAddBlockField is: re-marshalling reorders keys and scatters every
// "_comment_*" away from the setting it explains. The caller checks the result
// is valid JSON before anything is written.
func addBlockField(src, block, field, value string) (string, bool, error) {
	end := strings.LastIndex(src, "}")
	if end < 0 {
		return src, false, fmt.Errorf("%s has no closing brace to insert %q before", src, block)
	}
	head := strings.TrimRight(src[:end], " \t\r\n")
	if !strings.HasSuffix(head, "{") {
		head += ","
	}
	entry := fmt.Sprintf("\n\n  %q: {\n    %q: %s\n  }\n", block, field, value)
	return head + entry + src[end:], true, nil
}

// blockInFile reports which fields one top-level block states outright, as
// opposed to the ones config.Load fills in afterwards. The limits-block twin
// of limitsInFile, generalised: a missing block is not an error, it is a block
// that states nothing, which is exactly what "from default" means.
func blockInFile(path, block string) (map[string]json.RawMessage, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", path, err)
	}
	fields := map[string]json.RawMessage{}
	if raw, ok := doc[block]; ok {
		// A block that is not an object states no fields. Failing here would
		// make a malformed neighbour break the listing of everything else.
		_ = json.Unmarshal(raw, &fields)
	}
	return fields, nil
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
