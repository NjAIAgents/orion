package main

// `orion release ship` is the verb release.go reserved (OR-116).
//
// WHY `ship` AND NOT THE BARE NOUN. OR-116 asked for `orion release` to
// promote and publish. release.go's guard rail, written after it (OR-190),
// exists precisely so the irreversible meaning of the word can only be
// reached by NAMING it: a bare `orion release` prints usage and exits
// non-zero, so no half-typed command and no truncated script line ever
// arrives at a public tag. `publish`, `cut` and `ship` were reserved there,
// in that file, for this command. Spending one is what the reservation was
// for; taking the bare noun would give the guard rail away for nothing.
//
// WHY A COMMAND AND NOT A WORKFLOW. The approval already lives in Slack and
// the release credentials already live on this machine -- scripts/release.sh
// exists because Actions would need three personal access tokens to do what
// the operator's own authenticated gh already can. This composes those.
//
// WHY WATCH CANNOT REACH IT. Not by a flag: structurally. The only caller is
// release.go's dispatch, reached only from main's `release` case, and
// `orion watch` runs work and collect and nothing else. An unattended loop
// must not be able to cut a public release, and a guard that depends on a
// flag being read correctly is one refactor from being gone.
// TestWatchHasNoPathToShipping pins it.
//
// WHAT IT IS NOT. It does not decide WHETHER to release: that is the one
// decision the branch model reserves for a human (OR-115). It automates the
// ceremony around that decision -- preflight, promotion pull request, wait,
// ask, merge, cut -- and every step, INCLUDING EVERY REFUSAL, is an attributed
// event in the log. A failure at any step leaves everything inspectable,
// because an open pull request is just an open pull request.
//
// The refusals were the exception until OR-256: the log opened after the
// preflight, so the outcome this command produces most often was the one it
// recorded least. The one remaining gap is stated rather than papered over --
// a repository with no Orion binding still ships, on a nil log, with a
// warning. See shipContext.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/orion-sdlc/orion/internal/collect"
	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/promote"
	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/slack"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/work"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// shipPoll is how often the two waits re-read their subject. CI and a Slack
// reaction are both cheap to read and the operator is sitting in front of the
// terminal, so a slow poll would be felt as a hang.
const shipPoll = 10 * time.Second

func runReleaseShip(args []string) {
	w := os.Stdout
	beta := hasFlag(args, "--beta")
	dryRun := hasFlag(args, "--dry-run")
	rest := positional(args, "--wait", "--project")
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr,
			"orion release ship: which version? e.g. orion release ship v0.9.0")
		os.Exit(64)
	}

	root, err := os.Getwd()
	exitOn(err)
	cfg := config.Load(root)
	exitOn(cfg.Validate())
	if waiver := cfg.ReleaseBranchWaiver(); waiver != "" {
		ui.Warn(w, "%s", waiver)
	}
	// Not named `work`/`release`: internal/work is imported here, and a local
	// that shadows a package name is a compile error waiting for the next
	// person to add a call to it.
	workBranch, releaseBranch := cfg.VCS.WorkBranch, cfg.VCS.DefaultBranch

	channel := promote.Production
	if beta {
		channel = promote.Beta
	}

	// The remote's view, not this checkout's memory of it. Everything below
	// is about what WOULD ship, which is a property of origin.
	_ = gitOut(root, "fetch", "--quiet", "--tags", "origin")

	version := rest[0]
	if beta {
		version = promote.NextBeta(version, strings.Split(gitOut(root, "tag", "--list"), "\n"))
	}

	in := promote.ShipInputs{
		Channel:       channel,
		Version:       version,
		OnBranch:      gitOut(root, "rev-parse", "--abbrev-ref", "HEAD"),
		WorkBranch:    workBranch,
		ReleaseBranch: releaseBranch,
		Dirty:         gitOut(root, "status", "--porcelain") != "",
		Delta:         shipList(root, releaseBranch, workBranch),
		HeadSHA:       gitOut(root, "rev-parse", "origin/"+workBranch),
	}
	in.BuildSHA, in.BuildState = branchBuild(root, workBranch)

	// The ship list FIRST, before any refusal. It is the cheapest thing here
	// to read and the most expensive to be wrong about, and a preflight that
	// refuses without saying what it was looking at makes the operator run it
	// twice to learn one thing.
	ui.Ok(w, "ship", "%s (%s) would carry %d commit(s) from %s, cut from %s",
		version, channel, len(in.Delta), workBranch,
		promote.CutFrom(channel, workBranch, releaseBranch))
	for _, c := range in.Delta {
		fmt.Fprintf(w, "          %s\n", ui.Dim(w, c))
	}

	refusals := promote.ShipRefusals(in)
	if script := filepath.Join(root, "scripts", "release.sh"); !fileExists(script) {
		refusals = append(refusals,
			script+" is missing, so nothing can be published from here")
	}

	// The log OPENS BEFORE THE PREFLIGHT (OR-256). It used to open after, so
	// every refusal -- a dirty tree, red checks, an empty delta -- left no
	// event at all, and the command's commonest outcome was its least
	// recorded one. "Why didn't it ship last night?" is exactly the question
	// asked afterwards, when the terminal is gone and the log is all there is.
	//
	// A dry run stays out of the log deliberately: it is a question, not a
	// decision not to ship, and an event for it would read like one.
	channelID, log, closeLog := shipContext(root, argFlag(args, "--project", ""), w)
	defer closeLog()

	if len(refusals) > 0 {
		for _, r := range refusals {
			ui.Fail(w, "%s", r)
			// One event per guard rather than one for the set: which guard
			// fired is the whole content of the record, and a count is not
			// something anybody comes back to the log to learn.
			log.Emit(events.Event{Kind: events.KindBlocked, Actor: events.ActorOrion,
				Key: version, Msg: "refused to ship " + version + ": " + r})
		}
		ui.Fail(w, "%d refusal(s); nothing was promoted, tagged or published", len(refusals))
		os.Exit(1)
	}

	if dryRun {
		ui.Ok(w, "dry run", "the preflight passes. Nothing was promoted, tagged or published")
		return
	}

	log.Emit(events.Event{Kind: events.KindNote, Actor: events.ActorHuman,
		Msg: fmt.Sprintf("release ship %s started on the %s channel", version, channel)})

	if beta {
		shipBeta(root, version, log, w)
		return
	}
	shipProduction(root, cfg, in, channelID, waitFor(args), log, w)
}

// shipBeta cuts a prerelease from the integration branch.
//
// No promotion, no approval, no installer. A beta is evidence rather than a
// release: nothing an installed `brew upgrade` can reach changes, so there is
// nothing here for a second human to authorise that the operator did not
// authorise by typing the command.
func shipBeta(root, version string, log *events.Log, w io.Writer) {
	log.Emit(events.Event{Kind: events.KindNote, Actor: events.ActorOrion,
		Msg: "cutting " + version + " as a prerelease; the tap and the bucket are untouched"})
	if err := releaseScript(root, version, "--beta"); err != nil {
		log.Emit(events.Event{Kind: events.KindFailed, Actor: events.ActorOrion,
			Msg: "the beta cut failed: " + err.Error()})
		ui.Fail(w, "the beta cut failed: %v", err)
		os.Exit(1)
	}
	log.Emit(events.Event{Kind: events.KindNote, Actor: events.ActorOrion,
		Msg: "published " + version + " as a prerelease"})
}

// shipProduction runs the promotion ceremony, then cuts the release.
//
// The order is fixed and each step is a place it can stop safely: pull
// request, checks, ask, merge, cut. Only the last two cannot be taken back,
// and they are last for that reason.
func shipProduction(root string, cfg config.Config, in promote.ShipInputs,
	channelID string, wait time.Duration, log *events.Log, w io.Writer) {

	release := in.ReleaseBranch

	// 1. The promotion pull request. An open one from an interrupted run is
	// REUSED rather than duplicated -- that is what makes stopping here safe.
	//
	// The already-merged case is narrow but real: it is what the preflight's
	// empty-delta refusal sees a stale view of when the fetch above failed.
	// Skipping to the cut is right there; opening a second promotion for work
	// already on the release branch is not.
	pr, err := prStatus(root, in.WorkBranch)
	if err != nil {
		ui.Fail(w, "could not read the promotion pull request: %v", err)
		os.Exit(1)
	}
	switch pr.Verdict {
	case collect.VerdictUnknown, collect.VerdictClosed:
		title, body := promotionDescription(root, in)
		url, err := openPR(root, in.WorkBranch, title, body, release)
		if err != nil {
			ui.Fail(w, "could not open the promotion pull request: %v", err)
			os.Exit(1)
		}
		log.Emit(events.Event{Kind: events.KindPR, Actor: events.ActorOrion,
			Msg: "opened the promotion pull request " + url})
		ui.Ok(w, "pr", "promotion opened: %s", url)
		if pr, err = prStatus(root, in.WorkBranch); err != nil {
			ui.Fail(w, "opened %s but could not read it back: %v", url, err)
			os.Exit(1)
		}
	case collect.VerdictMerged:
		ui.Ok(w, "merged", "%s already promoted %s onto %s",
			pr.URL, in.WorkBranch, release)
	default:
		ui.Ok(w, "pr", "reusing the open promotion %s", pr.URL)
	}

	if pr.Verdict != collect.VerdictMerged {
		if pr.Conflicted {
			ui.Fail(w, "%s will not merge into %s without a human resolving a conflict",
				pr.URL, release)
			os.Exit(1)
		}

		// 2. The checks, on the pull request: the same wait every ticket gets.
		pr = awaitPRChecks(root, in.WorkBranch, pr, wait, log, w)
		if pr.Verdict != collect.VerdictPassing {
			log.Emit(events.Event{Kind: events.KindBlocked, Actor: events.ActorCI,
				Msg: "promotion checks " + string(pr.Verdict)})
			ui.Fail(w, "checks are %s on %s: %s", pr.Verdict, pr.URL, firstLineOf(pr.Detail))
			ui.Warn(w, "nothing was merged, tagged or published. The pull request is kept.")
			os.Exit(1)
		}
		ui.Stage(w, log, ui.Handoff{Key: in.Version, From: "ci", To: "approval",
			By: events.ActorCI, Next: events.ActorHuman,
			Detail: "checks pass on the promotion; asking in Slack"})

		// 3. The ask, then the merge.
		askAndMerge(root, cfg, in, pr, channelID, wait, log, w)
	}

	// 4. The cut. Everything from here leaves the release branch promoted if
	// it fails, which is a state worth naming rather than leaving to be
	// inferred from a stack of git commands that half worked.
	//
	// AND THAT INCLUDES CTRL-C (OR-257). The two await loops each install a
	// handler and each say something useful; this section had none, so an
	// interrupt during the cross-compile and upload -- the longest step by a
	// wide margin, and the one the operator is actually sitting watching --
	// killed the process on Go's default handling. Promoted, unreleased, and
	// not one word about it. The handler covers the whole irreversible
	// section so that the same message a failure gets is what an interrupt
	// gets, because the state they leave behind is identical.
	stop := onInterrupt(func() {
		shipStopped(w, log, release, in.Version,
			errors.New("interrupted during the cut"))
	})
	defer stop()

	if out, err := gitIn(root, "checkout", release); err != nil {
		shipStopped(w, log, release, in.Version,
			fmt.Errorf("could not check out %s: %s", release, firstLineOf(out)))
	}
	if out, err := gitIn(root, "pull", "--ff-only", "origin", release); err != nil {
		shipStopped(w, log, release, in.Version,
			fmt.Errorf("could not fast-forward %s: %s", release, firstLineOf(out)))
	}
	if err := releaseScript(root, in.Version); err != nil {
		shipStopped(w, log, release, in.Version, err)
	}
	log.Emit(events.Event{Kind: events.KindNote, Actor: events.ActorOrion,
		Msg: "published " + in.Version + " to the production channel"})
}

// shipStopped reports an abort AFTER the promotion merged, then exits.
//
// This is the one intermediate state a person cannot read off the console:
// the release branch has moved and no tag exists, so the repository looks
// released and nothing has been published. "It failed" leaves them to work
// out both halves. The resume command is printed because re-running this
// command would now refuse -- correctly -- for an empty delta.
func shipStopped(w io.Writer, log *events.Log, release, version string, err error) {
	ui.Fail(w, "%v", err)
	log.Emit(stoppedEvent(version, err))
	ui.Warn(w, "%s IS PROMOTED AND UNRELEASED: the merge landed, no tag was pushed, "+
		"and nothing was published.", release)
	fmt.Fprintf(w, "          %s\n", ui.Dim(w, fmt.Sprintf(
		"resume with: git checkout %s && git pull --ff-only && make release TAG=%s",
		release, version)))
	fmt.Fprintf(w, "          %s\n", ui.Dim(w,
		"re-running `orion release ship` would refuse: nothing is left to promote."))
	os.Exit(1)
}

// stoppedEvent is the record shipStopped leaves behind.
//
// Separated from the printing and the exit so that it can be asserted on: by
// the time anyone asks why a release stopped, this event is the only account
// of it that still exists. OR-256 established that a refusal must leave a
// record; OR-259 is the same principle one level down, because a record whose
// reason is "exit status 1" records that something happened and nothing about
// what.
//
// The output goes in Detail rather than Msg so that a log being scanned stays
// one line per event, and the evidence is still there for whoever opens it.
func stoppedEvent(version string, err error) events.Event {
	e := events.Event{Kind: events.KindFailed, Actor: events.ActorOrion,
		Msg: "stopped after the promotion merged, before " + version +
			" was tagged: " + err.Error()}
	var f *scriptFailure
	if errors.As(err, &f) && f.output != "" {
		e.Detail = map[string]any{"output": f.output}
	}
	return e
}

// scriptFailure is a failed scripts/release.sh run together with the tail of
// what it printed.
//
// The output has to travel with the error rather than only down the terminal.
// The script streams live, which is right for someone sitting watching it --
// but that stream is gone the moment the window is closed, and the failure
// this exists for is precisely the one nobody looks at until later.
type scriptFailure struct {
	err    error
	output string
}

func (e *scriptFailure) Error() string { return "scripts/release.sh: " + e.err.Error() }
func (e *scriptFailure) Unwrap() error { return e.err }

// gateTailBytes is how much of a failed release script's output is kept.
//
// Bounded because the thing being watched cross-compiles six targets and runs
// the whole test suite: all of it does not belong in a log line, and the end
// of it is the part that says why it stopped.
const gateTailBytes = 8 << 10

// tailWriter keeps the last gateTailBytes written to it and drops the rest.
//
// Mutexed because exec.Cmd copies stdout and stderr on separate goroutines
// when they are not *os.File, which is exactly what teeing makes them.
type tailWriter struct {
	mu sync.Mutex
	b  []byte
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > gateTailBytes {
		t.b = append([]byte(nil), t.b[len(t.b)-gateTailBytes:]...)
	}
	return len(p), nil
}

func (t *tailWriter) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.b))
}

// onInterrupt runs fn on the first SIGINT or SIGTERM, and returns a function
// that takes the handler back down.
//
// A goroutine rather than a select, because the section it guards is not a
// wait loop: it is a sequence of blocking calls, one of which is a
// cross-compile and upload that runs for minutes. There is nowhere to poll a
// channel from, so the signal has to arrive on its own thread and speak for
// itself (OR-257). fn is expected not to return -- shipStopped exits.
func onInterrupt(fn func()) (stop func()) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case <-sig:
			fn()
		case <-done:
		}
	}()
	return func() {
		signal.Stop(sig)
		close(done)
	}
}

// awaitPRChecks polls the promotion until its checks settle.
//
// Interruption is neither an error nor silent, for the reason it is not in
// collect: stopping the wait leaves an open pull request that is still
// perfectly valid, and the reasonable guess otherwise is that ctrl-c undid
// something.
func awaitPRChecks(root, branch string, pr collect.PR, wait time.Duration,
	log *events.Log, w io.Writer) collect.PR {

	defer func() {
		log.Emit(events.Event{Kind: events.KindCI, Actor: events.ActorCI,
			Msg: string(pr.Verdict) + " on the promotion: " + firstLineOf(pr.Detail)})
	}()
	if pr.Verdict != collect.VerdictPending || wait <= 0 {
		return pr
	}

	deadline := time.Now().Add(wait)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)

	fmt.Fprintf(w, "          %s\n", ui.Dim(w, fmt.Sprintf(
		"waiting up to %s for checks; ctrl-c to stop waiting", wait.Round(time.Second))))

	for pr.Verdict == collect.VerdictPending {
		if !time.Now().Before(deadline) {
			ui.Warn(w, "checks were still running after %s", wait.Round(time.Second))
			return pr
		}
		select {
		case <-stop:
			fmt.Fprintln(w)
			ui.Warn(w, "stopped waiting. The promotion pull request is open and unchanged.")
			return pr
		case <-time.After(shipPoll):
		}
		next, err := prStatus(root, branch)
		if err != nil {
			ui.Warn(w, "could not re-read the checks: %v", err)
			continue
		}
		pr = next
	}
	return pr
}

// askAndMerge posts the release approval, waits for an answer, and merges.
//
// Slack is REQUIRED here, unlike a ticket merge. Collect degrades to "checks
// pass, merge it yourself" when Slack is unusable, which is right for work
// that still has a human promotion step in front of it. This IS that step:
// degrading it to "Orion merged and published it because nobody could be
// asked" is the failure the whole branch model exists to prevent.
func askAndMerge(root string, cfg config.Config, in promote.ShipInputs, pr collect.PR,
	channelID string, wait time.Duration, log *events.Log, w io.Writer) {

	sc, err := slack.FromEnv()
	if err != nil || channelID == "" {
		ui.Fail(w, "a release must be approved in Slack, and there is no usable channel "+
			"to ask in (%v). Merge %s yourself, then: make release TAG=%s",
			err, pr.URL, in.Version)
		os.Exit(1)
	}

	tags, unresolved := collect.ApproverTags(sc, cfg.Slack.MergeApprovers)
	title, body := msgReleaseApproval(in, pr, tags)
	ts, err := sc.PostTS(channelID, title+"\n"+body)
	if err != nil {
		ui.Fail(w, "could not ask for release approval: %v", err)
		os.Exit(1)
	}
	for _, u := range unresolved {
		ui.Warn(w, "%s", u)
	}
	sc.React(channelID, ts, "white_check_mark")
	sc.React(channelID, ts, "x")
	log.Emit(events.Event{Kind: events.KindNote, Actor: events.ActorOrion,
		Msg: "asked for release approval in Slack for " + in.Version})
	ui.Ok(w, "asked", "%s: waiting for a release approval in Slack", in.Version)

	d := awaitReleaseDecision(sc, channelID, ts, cfg.Slack.MergeApprovers, wait, w)
	switch {
	case d.Rejected:
		log.Emit(events.Event{Kind: events.KindBlocked, Actor: events.ActorHuman,
			Msg: d.By + " declined the release"})
		ui.Warn(w, "%s declined the release (%s). Nothing was merged or published.", d.By, d.How)
		os.Exit(1)
	case !d.Approved:
		log.Emit(events.Event{Kind: events.KindBlocked, Actor: events.ActorHuman,
			Msg: "nobody approved the release: " + d.Why})
		ui.Warn(w, "no approval: %s. Nothing was merged or published.", d.Why)
		fmt.Fprintf(w, "          %s\n", ui.Dim(w, fmt.Sprintf(
			"the request is still in Slack and still valid -- react there, then run "+
				"`orion release ship %s` again.", in.Version)))
		os.Exit(1)
	}

	ui.Stage(w, log, ui.Handoff{Key: in.Version, From: "approval", To: "promotion",
		By: events.ActorHuman, Next: events.ActorOrion,
		Detail: fmt.Sprintf("%s approved the release (%s)", d.By, d.How)})

	// --merge, NOT the configured ticket strategy. A promotion is the one
	// merge whose shape is not a matter of taste: rebase or squash replays
	// the integration branch's commits onto the release branch as new
	// objects, so the two branches share no history and every later promotion
	// re-reports the same commits as unmerged forever.
	if err := mergePR(root, in.WorkBranch, fmt.Sprintf(
		"Release %s approved by %s in Slack (%s).", in.Version, d.By, d.How),
		"merge"); err != nil {
		log.Emit(events.Event{Kind: events.KindFailed, Actor: events.ActorOrion,
			Msg: "the promotion merge failed: " + err.Error()})
		ui.Fail(w, "approved by %s, but the promotion merge failed: %v", d.By, err)
		ui.Warn(w, "nothing was tagged or published. The approval stands; "+
			"re-run to retry the merge.")
		os.Exit(1)
	}
	log.Emit(events.Event{Kind: events.KindMerge, Actor: events.ActorHuman,
		Msg: "promoted " + in.WorkBranch + " onto " + in.ReleaseBranch +
			" on " + d.By + "'s approval"})
	ui.Ok(w, "promoted", "%s onto %s on %s's approval",
		in.WorkBranch, in.ReleaseBranch, d.By)
}

// awaitReleaseDecision polls the request until somebody answers.
//
// Waiting is the default and there is no second pass. A ticket's approval can
// be read by the next watcher tick; this one cannot, because watch has no
// path to shipping at all -- so exiting after asking would leave the answer
// permanently unread.
func awaitReleaseDecision(sc *slack.Client, channelID, ts string, approvers []string,
	wait time.Duration, w io.Writer) collect.Decision {

	read := func() collect.Decision {
		d, err := collect.ReadDecision(sc, channelID, ts, sc.BotID(), approvers)
		if err != nil {
			ui.Warn(w, "could not read the approval: %v", err)
		}
		return d
	}
	d := read()
	if wait <= 0 || d.Approved || d.Rejected {
		return d
	}

	deadline := time.Now().Add(wait)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)

	fmt.Fprintf(w, "          %s\n", ui.Dim(w, fmt.Sprintf(
		"waiting up to %s for a reaction; ctrl-c to stop waiting", wait.Round(time.Second))))

	for {
		if !time.Now().Before(deadline) {
			d.Why = fmt.Sprintf("nobody answered within %s", wait.Round(time.Second))
			return d
		}
		select {
		case <-stop:
			fmt.Fprintln(w)
			d.Why = "you stopped waiting"
			return d
		case <-time.After(shipPoll):
		}
		if d = read(); d.Approved || d.Rejected {
			return d
		}
	}
}

// msgReleaseApproval asks for authority over a PUBLIC artifact.
//
// It names the version, says this is a release rather than a ticket merge,
// and lists what ships. A ticket approval gets away with linking the diff,
// because declining one costs a branch. Approving this publishes a tag, a
// Homebrew formula and a Scoop manifest that every installed user upgrades
// into, and the reader has to be able to refuse on the strength of the
// message alone.
func msgReleaseApproval(in promote.ShipInputs, pr collect.PR, approvers []string) (string, string) {
	title := fmt.Sprintf("RELEASE %s — promote %s to %s and publish?",
		in.Version, in.WorkBranch, in.ReleaseBranch)

	who := "nobody is configured, so no approval can succeed"
	if len(approvers) > 0 {
		who = strings.Join(approvers, ", ")
	}

	// The whole list, not a sample. This is the one message where "and 24
	// more" would hide the commit the reader was going to object to.
	var ships []string
	for _, c := range in.Delta {
		ships = append(ships, "• "+c)
	}

	body := strings.Join([]string{
		fmt.Sprintf("*This is a production release, not a ticket merge.* Approving it "+
			"promotes `%s` onto `%s`, tags `%s`, and updates the Homebrew tap and the "+
			"Scoop bucket — every installed user upgrades into it.",
			in.WorkBranch, in.ReleaseBranch, in.Version),
		"",
		fmt.Sprintf("*What ships (%d commit(s)):*", len(in.Delta)),
		strings.Join(ships, "\n"),
		"",
		"• promotion  <" + pr.URL + "|pull request>",
		"",
		"React ✅ to publish, or ❌ to decline. Replying `approve` or `no` works too.",
		"",
		"_Only " + who + " can approve._",
		"_Declining changes nothing: the pull request is kept and no tag is pushed._",
	}, "\n")
	return title, body
}

// promotionDescription writes the promotion pull request's title and body.
//
// Through work.DescribePR -- the same describer and the same seam every
// ticket pull request goes through -- so the promotion is not the one pull
// request in the repository whose body was written by a template. The commit
// list is the fallback AND the trailer, so what ships is in the body whether
// or not the describer ran.
func promotionDescription(root string, in promote.ShipInputs) (string, string) {
	title, body := promotionFallback(in)
	t, b, _ := work.DescribePR(describeRunner, root, "release "+in.Version, title, body)
	return t, b
}

// promotionFallback is the description Orion writes itself: what ships, in
// full, from the delta it already has. Separate from the describer call so it
// is assertable without spawning an agent -- and because it is the version
// that ends up in the pull request whenever the describer cannot run, which
// on a machine without the CLI is every time.
func promotionFallback(in promote.ShipInputs) (string, string) {
	title := fmt.Sprintf("Release %s: promote %s to %s",
		in.Version, in.WorkBranch, in.ReleaseBranch)
	body := strings.Join([]string{
		fmt.Sprintf("Promotes `%s` onto `%s` for release `%s`.",
			in.WorkBranch, in.ReleaseBranch, in.Version),
		"",
		fmt.Sprintf("## What ships (%d commit(s))", len(in.Delta)),
		"",
		"```",
		strings.Join(in.Delta, "\n"),
		"```",
		"",
		"Opened by `orion release ship`. Merging it is gated on a Slack approval.",
	}, "\n")
	return title, body
}

// shipList is what this release would carry: the commits the integration
// branch has that the release branch does not.
func shipList(root, releaseBranch, workBranch string) []string {
	var out []string
	raw := gitOut(root, "log", "--format=%h %s",
		"origin/"+releaseBranch+"..origin/"+workBranch)
	for _, line := range strings.Split(raw, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// shipContext resolves the Slack channel to ask in and the event log to
// record into, from the registry binding for this repository.
//
// Both come from the same lookup because they are the same question -- which
// adopted project is this -- and asking it twice is how the release ends up
// announced in one project's channel and logged under another's.
//
// A repository with no binding still ships. It gets a warning and a nil log
// rather than a refusal: Emit is nil-safe, and refusing to release because
// the audit trail has nowhere to go would be a worse trade than releasing
// with the gap named out loud.
func shipContext(root, project string, w io.Writer) (channelID string, log *events.Log, done func()) {
	done = func() {}

	reg, err := registry.Load(workspace.Home())
	if err != nil {
		ui.Warn(w, "no registry, so this release leaves no event trail: %v", err)
		return "", nil, done
	}
	key, entry := "", registry.Entry{}
	for k, e := range reg.Repos {
		if (project != "" && strings.EqualFold(k, project)) ||
			(project == "" && sameRoot(e.Source, root)) {
			key, entry = k, e
			break
		}
	}
	if key == "" {
		ui.Warn(w, "%s is not an adopted project, so this release leaves no event "+
			"trail and cannot be approved in Slack", root)
		return "", nil, done
	}

	ws, err := workspace.Open(entry.Workspace)
	if err != nil {
		ui.Warn(w, "%s has no readable workspace, so this release leaves no event "+
			"trail: %v", key, err)
		return entry.Channel, nil, done
	}
	l, err := events.Open(events.Path(ws.Dir), events.Event{
		Project: key, Key: key,
		Run:   fmt.Sprintf("%d", time.Now().UnixNano()),
		Actor: events.ActorOrion,
	})
	if err != nil {
		ui.Warn(w, "could not open the event log, so this release leaves no trail: %v", err)
		return entry.Channel, nil, done
	}
	return entry.Channel, l, func() { _ = l.Close() }
}

func sameRoot(a, b string) bool {
	ax, err1 := filepath.EvalSymlinks(a)
	bx, err2 := filepath.EvalSymlinks(b)
	if err1 != nil || err2 != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ax == bx
}

// releaseScript runs scripts/release.sh, streaming its output and keeping a
// copy of the tail.
//
// Unbounded on purpose, unlike every gh call on the watch path: it
// cross-compiles six targets and uploads them, which legitimately takes
// minutes, and it is only ever run attended.
//
// Tee rather than capture (OR-259). The operator is watching this live, so
// the stream has to stay on the terminal; the copy is for the event log,
// which is what is left once the terminal is not.
func releaseScript(root, version string, extra ...string) error {
	cmd := exec.Command(filepath.Join(root, "scripts", "release.sh"),
		append([]string{version}, extra...)...)
	cmd.Dir = root
	tail := &tailWriter{}
	cmd.Stdout = io.MultiWriter(os.Stdout, tail)
	cmd.Stderr = io.MultiWriter(os.Stderr, tail)
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		return &scriptFailure{err: err, output: tail.String()}
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
