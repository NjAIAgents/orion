package collect

// The call site for batch integration (OR-236): what collect.Run does with a
// pass when collect.batch_integration is on.
//
// A separate function rather than branches threaded through one(). The
// per-branch path is what every repository uses today and what runs when the
// flag is off, and interleaving the two would make the old path's behaviour
// depend on a feature that is not enabled. Off, nothing here is reached.
//
// What this does NOT do is merge. Land() reports which members a green batch
// would land, and the existing approval and merge path acts on that, so the
// one irreversible step in this package stays in the one place that already
// owns it.

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// batchTester runs the forge's checks against an assembled ref.
//
// It publishes the ref and waits for the checks the repository already runs.
// Nothing here decides what "green" means: that is the same all-checks-passing
// rule the per-branch path uses, asked about a different ref.
type batchTester struct {
	git    repoGit
	status func(dir, branch string) (PR, error)
	dir    string
	wait   time.Duration
	out    io.Writer
	log    *events.Log
}

// Test publishes ref and reports whether its checks pass.
//
// An empty rollup is NOT passing here, and that is the one place this differs
// from the per-branch path deliberately. cmd/orion/collect.go treats no checks
// as VerdictPassing with the note "no checks are configured on this
// repository" -- right for a repository without CI, and catastrophic for a ref
// whose checks have simply not started yet. Under a merge ref every member
// would read green on no evidence at all, which is precisely how ADR 0015 says
// this gate disappears.
func (t batchTester) Test(ref string) (bool, error) {
	if err := t.git.PushRef(ref); err != nil {
		return false, err
	}
	defer func() { _ = t.git.DeleteRemoteRef(ref) }()

	deadline := time.Now().Add(t.wait)
	for {
		pr, err := t.status(t.dir, ref)
		if err != nil {
			return false, fmt.Errorf("reading the checks on %s: %w", ref, err)
		}
		switch {
		case pr.Verdict == VerdictFailing:
			return false, nil
		case pr.Verdict == VerdictPassing && !noChecksYet(pr):
			return true, nil
		case pr.Verdict == VerdictPassing:
			// Silence is not success. Keep waiting; the deadline below ends
			// this, not an absence of evidence.
		}
		if time.Now().After(deadline) {
			return false, fmt.Errorf(
				"no check result for %s after %s; refusing to read silence as green", ref, t.wait)
		}
		time.Sleep(15 * time.Second)
	}
}

// noChecksYet reports the empty rollup that cmd/orion/collect.go turns into a
// PASSING verdict with this exact note.
//
// Matching the note rather than a count because PR carries no count -- and it
// is worth the fragility, because the alternative is reading "no checks have
// started" as "every check passed". Under a merge ref that would mark every
// member green on no evidence, which ADR 0015 names as the way this gate
// disappears the day it ships.
func noChecksYet(pr PR) bool {
	return strings.Contains(strings.ToLower(pr.Detail), "no checks are configured")
}

// runBatch lands the pass as one set.
//
// Returns a Result per ticket so the caller's contract is unchanged: the
// watcher reports the same shape whether the batch path or the per-branch path
// produced it.
func runBatch(pass []string, cfg config.Config, opts Options, deps Deps,
	ws *workspace.Workspace, log *events.Log, w io.Writer) []Result {

	// The concurrency limit IS the batch size. Nothing can be in a batch
	// that did not finish, and nothing finishes that was not allowed to run.
	size := cfg.Limits.ConcurrentTickets()

	var members []Member
	for _, key := range pass {
		if len(members) >= size {
			break
		}
		// The branch a job actually RECORDED, never the one convention
		// predicts: AddWorktree suffixes a retry (orion/or-156-2) to keep it
		// off a prior attempt's open pull request, and recomputing the name
		// polls a branch that does not exist (OR-173).
		branch, recorded := workspace.BranchOf(ws, key)
		if !recorded {
			branch = branchFor(cfg.VCS.BranchPrefix, key)
		}
		pr, err := deps.Status(ws.CloneDir(), branch)
		if err != nil || pr.URL == "" || pr.Verdict == VerdictMerged {
			continue // not ready to land; the per-branch path still reports it
		}
		members = append(members, Member{Key: key, Branch: branch, Head: pr.Head})
	}
	if len(members) == 0 {
		return nil
	}

	ref := "orion/batch"
	g := repoGit{ws: ws}
	if opts.DryRun {
		ui.Ok(w, "would", "assemble %d branch(es) into %s and test once: %s",
			len(members), ref, strings.Join(pass[:len(members)], " "))
		return nil
	}

	ui.Say(w, "", events.ActorOrion, ui.VerbWorking,
		"assembling %d branch(es) into %s", len(members), ref)

	t := batchTester{git: g, status: deps.Status, dir: ws.CloneDir(),
		wait: 30 * time.Minute, out: w, log: log}
	b, err := Land(g, t, ref, cfg.VCS.WorkBranch, members)
	_ = g.DropRef(ref)

	for _, line := range b.Describe() {
		fmt.Fprintf(w, "          %s\n", ui.Dim(w, line))
	}
	ui.Say(w, "", events.ActorOrion, ui.VerbOK,
		"the batch cost %d CI run(s) for %d branch(es)", b.Runs, len(members))
	log.Emitf(events.KindNote, events.ActorOrion,
		"batch on %s: %d run(s), landed=%v ejected=%v culprit=%v deferred=%v",
		ref, b.Runs, b.Members(Landed), b.Members(Ejected),
		b.Members(Culprit), b.Members(Deferred))

	var out []Result
	for _, r := range b.Results {
		res := Result{Key: r.Key, Changed: true}
		switch r.Outcome {
		case Landed:
			// Green as a SET. The existing per-branch pass now takes it
			// through approval and merge, which keeps the irreversible step
			// where it already lives.
			res.Verdict = VerdictPassing
		case Culprit:
			res.Verdict = VerdictFailing
		default:
			// Ejected and deferred are not failures: the branch is sound and
			// will be offered to the next batch. Saying "stale" reuses the
			// verdict the watcher already renders as waiting.
			res.Verdict = VerdictStale
			res.Changed = false
		}
		out = append(out, res)
	}
	if err != nil {
		ui.Warn(w, "the batch did not complete: %v", err)
		out = append(out, Result{Err: err})
	}
	return out
}

// batchContext resolves the workspace, log and config a batch needs.
//
// From the FIRST ticket in the pass. A batch is assembled in one repository's
// sandbox, so a pass spanning two projects has no single batch to build; using
// one project's config for the set is therefore accurate rather than merely
// convenient, and the members loop skips anything whose branch does not
// resolve in that workspace.
//
// Config comes from entry.Source -- the user's checkout -- never the sandbox
// clone, for the reason one() gives: read work_branch from a stale sandbox and
// you sync the sandbox to the stale branch, which makes the value that decides
// where things land depend on where things last landed.
func batchContext(pass []string, opts Options, deps Deps) (
	*workspace.Workspace, *events.Log, io.Writer, config.Config, bool) {

	w := opts.Out
	if len(pass) == 0 {
		return nil, nil, w, config.Config{}, false
	}
	entry, err := registry.Lookup(opts.Home, pass[0])
	if err != nil {
		return nil, nil, w, config.Config{}, false
	}
	ws, err := workspace.Open(entry.Workspace)
	if err != nil {
		return nil, nil, w, config.Config{}, false
	}
	cfg := config.Load(entry.Source)
	// The same shape one() uses: events.Open returns a usable log either way,
	// and a batch without a written record is still a batch worth running --
	// the console reports it regardless. Refusing over bookkeeping would lose
	// the work to protect the note about it.
	// NOT closed here. The caller writes the batch's record through it after
	// this returns, so closing on the way out would shut the log before the
	// only thing that uses it has run.
	log, _ := events.Open(events.Path(ws.Dir), events.Event{
		Project: registry.ProjectOf(pass[0]), Actor: events.ActorOrion,
	})
	return ws, log, w, cfg, true
}
