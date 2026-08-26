// Package report summarises what Orion has been doing.
//
// It exists because per-run logs answer "what happened in this run" and
// nothing answered "what has been happening". A supervisor you have to
// interrogate one workspace at a time is one you stop checking.
//
// The output is deliberately plain text with no colour or box drawing, so
// the same bytes are readable in a terminal, in a cron mail, and in a Slack
// message. A report that needs a renderer is a report that does not travel.
package report

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/budget"
	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// Digest is the whole picture at one moment.
type Digest struct {
	Generated  time.Time
	Since      time.Time
	Workspaces []WorkspaceLine
	Failures   []Failure
	Budget     budget.Status
	Usage      budget.TranscriptUsage
	Attention  []string
	// Only records the filter that produced this digest, so the rendered
	// output can say it is partial. A narrowed report that looks complete is
	// how someone concludes nothing else is running.
	Only string
}

// WorkspaceLine is one project's state.
type WorkspaceLine struct {
	ID       string
	Stage    string
	Status   string
	Idea     string
	LastRun  time.Time
	Runs     int
	Failed   int
	ResumeAt time.Time
}

// Failure is a run that did not succeed, with enough context to act.
type Failure struct {
	Workspace string
	Stage     string
	At        time.Time
	Reason    string
	Log       string
}

// Build assembles the digest. Errors are folded into Attention rather than
// returned: a digest that refuses to render because one workspace is
// unreadable is worse than one that says so.
// Build assembles the digest, optionally narrowed to one project.
//
// `only` accepts what a person has in their hand: a project key (FCIA), an
// issue key (FCIA-6) or a workspace id. Empty means everything. Matching the
// same inputs as `orion logs` is deliberate -- two commands that describe the
// same work should not disagree about how to name it.
func Build(home string, since time.Time, lim budget.Limits, only string) *Digest {
	d := &Digest{Generated: time.Now(), Since: since, Only: only}

	ids, err := workspace.IDs()
	if err != nil {
		d.Attention = append(d.Attention, "could not list workspaces: "+err.Error())
	}
	want := resolveFilter(home, only)
	for _, id := range ids {
		if want != "" && id != want {
			continue
		}
		ws, openErr := workspace.Open(id)
		if openErr != nil {
			d.Attention = append(d.Attention, "workspace "+id+" unreadable: "+openErr.Error())
			continue
		}
		line := WorkspaceLine{
			ID: ws.ID, Stage: ws.Task.Stage, Status: ws.Task.Status,
			Idea: ws.Task.Idea, ResumeAt: ws.Task.ResumeAt,
		}
		for _, r := range ws.Task.Runs {
			line.Runs++
			if r.StartedAt.After(line.LastRun) {
				line.LastRun = r.StartedAt
			}
			if r.ExitCode != 0 && r.StartedAt.After(since) {
				line.Failed++
				d.Failures = append(d.Failures, Failure{
					Workspace: ws.ID, Stage: r.Stage, At: r.StartedAt,
					Reason: r.Reason, Log: r.Log,
				})
			}
		}
		// A workspace parked on a quota wall is the single thing most likely
		// to be silently forgotten, so it is called out by name.
		if !ws.Task.ResumeAt.IsZero() && ws.Task.ResumeAt.After(time.Now()) {
			d.Attention = append(d.Attention, fmt.Sprintf(
				"%s is waiting on a quota reset until %s",
				ws.ID, ws.Task.ResumeAt.Local().Format("15:04 MST")))
		}
		d.Workspaces = append(d.Workspaces, line)
	}

	sort.Slice(d.Workspaces, func(i, j int) bool {
		return d.Workspaces[i].LastRun.After(d.Workspaces[j].LastRun)
	})
	sort.Slice(d.Failures, func(i, j int) bool { return d.Failures[i].At.After(d.Failures[j].At) })

	ledger, ledgerErr := budget.Load(home)
	if ledgerErr != nil {
		d.Attention = append(d.Attention, ledgerErr.Error())
	}
	d.Budget = ledger.Status(lim)
	if d.Budget.Crossed > 0 {
		d.Attention = append(d.Attention, fmt.Sprintf(
			"budget checkpoint %d%% is unacknowledged; runs are stopped until `orion budget ack %d`",
			d.Budget.Crossed, d.Budget.Crossed))
	}

	if u, scanErr := budget.ScanTranscripts(budget.TranscriptDir(), since); scanErr == nil {
		d.Usage = u
	}
	return d
}

// resolveFilter turns a project key, issue key or workspace id into the
// workspace id to keep, or "" for everything.
//
// Unresolvable input yields a sentinel that matches nothing rather than
// falling back to "show everything". Silently widening a filter someone
// typed wrong would answer a question they did not ask, and they would read
// the result as though they had.
func resolveFilter(home, only string) string {
	only = strings.TrimSpace(only)
	if only == "" {
		return ""
	}
	if e, err := registry.Lookup(home, only); err == nil {
		return e.Workspace
	}
	if ids, err := workspace.IDs(); err == nil {
		for _, id := range ids {
			if strings.EqualFold(id, only) || strings.HasPrefix(id, only) {
				return id
			}
		}
	}
	return "\x00no-such-workspace"
}

// Text renders the digest. Kept narrow enough to survive Slack's wrapping.
func (d *Digest) Text() string {
	var b strings.Builder
	window := time.Since(d.Since).Round(time.Hour)
	fmt.Fprintf(&b, "orion report  %s  (last %s)\n",
		d.Generated.Local().Format("2006-01-02 15:04"), window)
	if d.Only != "" {
		// Say the report is narrowed. Otherwise an empty section reads as
		// "nothing is running" rather than "nothing matched your filter".
		fmt.Fprintf(&b, "filtered to %s\n", d.Only)
	}

	// Attention first. Burying the actionable part under a status table is
	// how a digest becomes wallpaper.
	if len(d.Attention) > 0 {
		b.WriteString("\nNEEDS ATTENTION\n")
		for _, a := range d.Attention {
			fmt.Fprintf(&b, "  - %s\n", a)
		}
	}

	if len(d.Failures) > 0 {
		fmt.Fprintf(&b, "\nFAILURES (%d)\n", len(d.Failures))
		for i, f := range d.Failures {
			if i == 8 {
				fmt.Fprintf(&b, "  ... and %d more\n", len(d.Failures)-8)
				break
			}
			fmt.Fprintf(&b, "  %s  %-9s %s\n", f.At.Local().Format("Jan 02 15:04"), f.Stage, f.Workspace)
			if f.Reason != "" {
				fmt.Fprintf(&b, "      %s\n", truncate(f.Reason, 90))
			}
			if f.Log != "" {
				fmt.Fprintf(&b, "      log: %s\n", f.Log)
			}
		}
	} else {
		b.WriteString("\nno failures in the window\n")
	}

	if len(d.Workspaces) > 0 {
		b.WriteString("\nWORKSPACES\n")
		for i, w := range d.Workspaces {
			if i == 10 {
				fmt.Fprintf(&b, "  ... and %d more (orion ls)\n", len(d.Workspaces)-10)
				break
			}
			last := "never"
			if !w.LastRun.IsZero() {
				last = w.LastRun.Local().Format("Jan 02 15:04")
			}
			fmt.Fprintf(&b, "  %-34s %-9s %-11s %s\n",
				truncate(w.ID, 34), truncate(w.Stage, 9), truncate(w.Status, 11), last)
		}
	}

	b.WriteString("\nUSAGE (all Claude Code sessions in the window)\n")
	if d.Usage.Turns > 0 {
		fmt.Fprintf(&b, "  %d sessions, %d turns (%d subagent)\n",
			d.Usage.Sessions, d.Usage.Turns, d.Usage.Sidechain)
		fmt.Fprintf(&b, "  effective %s  (raw %s; cache reads dominate raw volume)\n",
			human(d.Usage.Tokens.Effective()), human(d.Usage.Tokens.Total()))
	} else {
		b.WriteString("  no transcript activity found\n")
	}
	if d.Budget.Limits.Set() {
		fmt.Fprintf(&b, "  budget    %d%% of your weekly limit ($%.2f)\n",
			d.Budget.Percent, d.Budget.SpentUSD)
	} else {
		fmt.Fprintf(&b, "  spend     $%.2f (no weekly budget set)\n", d.Budget.SpentUSD)
	}

	return b.String()
}

// Healthy reports whether anything needs a human. Used as the exit code so
// cron can stay quiet when all is well.
func (d *Digest) Healthy() bool { return len(d.Failures) == 0 && len(d.Attention) == 0 }

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func human(n int) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.2fB", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.0fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

// TailLog returns the last n lines of a run log, which is what someone
// actually wants when a stage failed.
func TailLog(path string, n int) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n"), nil
}

// LogsFor lists a workspace's run logs, newest first.
func LogsFor(ws *workspace.Workspace) ([]string, error) {
	entries, err := os.ReadDir(ws.LogsDir())
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".log") {
			out = append(out, filepath.Join(ws.LogsDir(), e.Name()))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out, nil
}
