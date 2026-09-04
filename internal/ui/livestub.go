package ui

// The live region is gone (OR-334). What is left is the seam.
//
// For two days a pinned, redrawn terminal region -- ticket rows, a frozen
// scrollback window, a batch block with a chain and a job list -- cost more
// than it returned. Six defects came out of the rendering itself (OR-307,
// OR-308, OR-313, OR-317, OR-319, OR-330), each one costing a release and a
// watch run to find, and none of them was about whether Orion did its work
// correctly. A watch that prints a plain sequential log cannot strand a
// frame, cannot miscount an erase, and cannot hide a batch behind a region
// that failed to draw.
//
// EVERYTHING ELSE IS UNCHANGED. Fan-out, subagents, the batch queue, the QA
// suite runner, the fix loop -- none of that lived here. This file keeps the
// call sites compiling and doing nothing, so removing the display did not
// mean editing collect, watch and work on the same evening. The narration
// those packages already emit through ui.Say/Warn/Ok IS the interface now,
// and it always was: every line the region showed was also printed.
//
// The types stay because callers name them (ui.Check, ui.MemberLanded). They
// are data, not display, and internal/collect maps its own vocabulary onto
// them.
//
// If a live view is ever wanted again, it should be a separate command
// reading the event log -- not a writer wrapper that has to agree with the
// terminal about how many rows it drew.

import (
	"io"
	"os"
	"sync"
	"time"
)

// BatchPhase is which of the phases a batch is in. Kept as data: collect
// reports phases and the event log records them.
type BatchPhase string

const (
	BatchAssembling BatchPhase = "assembling"
	BatchTesting    BatchPhase = "testing"
	BatchIsolating  BatchPhase = "isolating"
	BatchDone       BatchPhase = "done"
)

// MemberState is what became of one branch in a batch.
type MemberState string

const (
	MemberPending MemberState = "pending"
	MemberWorking MemberState = "working"
	MemberMerged  MemberState = "merged"
	MemberEjected MemberState = "ejected"
	MemberLanded  MemberState = "landed"
	MemberCulprit MemberState = "culprit"
)

// Check is one CI check and where it got to.
type Check struct {
	Name  string
	State string
}

// The check states, matching internal/collect's vocabulary.
const (
	CheckPassed  = "passed"
	CheckFailed  = "failed"
	CheckRunning = "running"
)

// QueueRow is one ticket the tracker says Orion is responsible for.
type QueueRow struct {
	Key, Stage, Title string
}

// Live is a pass-through writer.
//
// It stays a type rather than becoming a bare io.Writer because the watch
// holds one, writes through it, and closes it on the way out. Nothing is
// buffered, nothing is redrawn: a line written here is a line on the
// terminal, in the order it was written.
type Live struct{ w io.Writer }

// NewLive wraps a writer. The wrapper does nothing but pass bytes along.
func NewLive(w io.Writer) *Live { return &Live{w: w} }

func (l *Live) Write(p []byte) (int, error) {
	if l == nil || l.w == nil {
		return len(p), nil
	}
	return l.w.Write(p)
}

// Close is a no-op: there is no region to erase and nothing to commit to
// scrollback, because every line was already printed as it happened.
func (l *Live) Close() {}

// Tick was the off-terminal path's per-interval summary. The log is now the
// only path, and it prints as work happens rather than on a timer.
func (l *Live) Tick() {}

// Full and ToggleCollapsed were the ctrl-r and ctrl-o keys. There is no
// region to expand or collapse.
func (l *Live) Full()            {}
func (l *Live) ToggleCollapsed() {}

// The console writer survives the region's removal (OR-330, kept by OR-334).
//
// It is not display: it is the answer to "where does a message meant for a
// person go". The supervisor and the fan narrate through it, and before
// OR-330 they wrote to os.Stderr directly. That is orthogonal to whether a
// region is drawn -- the watch still owns stdout, and a writer that agrees
// on the destination is worth keeping whatever is being written.
var termConsole struct {
	mu sync.Mutex
	w  io.Writer
}

// SetConsole installs the writer the process should narrate through; nil
// restores os.Stderr.
func SetConsole(w io.Writer) {
	termConsole.mu.Lock()
	termConsole.w = w
	termConsole.mu.Unlock()
}

// Console is where a message meant for the terminal goes.
func Console() io.Writer {
	termConsole.mu.Lock()
	defer termConsole.mu.Unlock()
	if termConsole.w == nil {
		return os.Stderr
	}
	return termConsole.w
}

// ConsoleEngaged reports whether the watch owns the terminal.
func ConsoleEngaged() bool {
	termConsole.mu.Lock()
	defer termConsole.mu.Unlock()
	return termConsole.w != nil
}

// The live API, kept as no-ops so no caller had to change (OR-334). Each one
// described something the region drew; the same facts reach the operator as
// log lines from the packages that call these.
func LiveReset()                                          {}
func LiveWindowCap(int)                                   {}
func LiveMedians(func(string) time.Duration)              {}
func LiveStart(string)                                    {}
func LiveEnd(string)                                      {}
func LiveDone(string, string)                             {}
func LiveStage(string, string, string)                    {}
func LiveTitle(string, string)                            {}
func LiveActivity(string, string)                         {}
func LiveActivityNote(string, string, string)             {}
func LiveAgents(string)                                   {}
func LiveSpend(float64)                                   {}
func LiveCI(int)                                          {}
func LiveChecks([]Check)                                  {}
func LiveQueue([]QueueRow)                                {}
func LiveBatchStart(string, string, []string)             {}
func LiveBatchPhase(BatchPhase)                           {}
func LiveBatchMember(string, MemberState)                 {}
func LiveBatchMemberDetail(string, MemberState, string)   {}
func LiveBatchMemberCost(string, time.Duration, float64)  {}
func LiveBatchSplit([]string, bool, int, int, bool)       {}
func LiveBatchMedian(time.Duration)                       {}
func LiveBatchResume(string, string, []string, time.Time) {}
func LiveBatchEnd()                                       {}

// LiveAgentCount answered "how many subagents does this ticket have running".
// Nothing counts them now; the callers use it only to decide whether to
// mention a number.
func LiveAgentCount(string) int { return 0 }
