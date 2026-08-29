// Package workspace provisions isolated project directories.
//
// Every task Orion is given gets its own directory tree, its own git
// repository, its own generated settings and its own state. Nothing an
// agent does in one workspace can reach another, and nothing it does
// reaches the user's other work at all.
//
// Isolation here is layered, and the layers are worth naming honestly:
//
//	directory   a dedicated tree under $ORION_HOME, never the user's cwd
//	git         a fresh repo, so a bad commit cannot touch existing history
//	settings    generated per workspace: permission denies plus the OS
//	            sandbox (Seatbelt on macOS, namespaces on Linux)
//	container   opt-in, for code you do not trust
//
// The OS sandbox stops credential reads and network egress. It is not a
// VM. For genuinely untrusted code use --container.
package workspace

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/orion-sdlc/orion/internal/provision"
)

// Task is the durable record of what a workspace is for. It is the
// provenance trail: what was asked, when, by which Orion version, and
// how far through the artifact chain it got.
type Task struct {
	ID        string    `json:"id"`
	Idea      string    `json:"idea"`
	Slug      string    `json:"slug"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Stage     string    `json:"stage"`
	Status    string    `json:"status"`
	Container bool      `json:"container"`
	FromRepo  string    `json:"from_repo,omitempty"`
	// SourcePath is the user's own working copy, for a workspace bound to an
	// existing repository. Orion clones from the remote and treats this path
	// as READ-ONLY; it is recorded so the copy can be fast-forwarded after a
	// push, never written to during a run.
	SourcePath string `json:"source_path,omitempty"`
	// ResumeAt is set when a run stopped on a provider quota wall. It is a
	// record, not a schedule: nothing sleeps on it, and the user or a cron
	// decides when to actually come back.
	ResumeAt time.Time `json:"resume_at,omitempty"`
	// Branches created at provisioning: main (release, protected) and
	// develop (integration, the pull-request base).
	Branches []string `json:"branches,omitempty"`
	// Slack is the project's channel, when one was created. It is the medium
	// the run reports into.
	Slack *SlackChannel `json:"slack,omitempty"`
	// Remote and Tracker are filled by the provision stage.
	Remote  string          `json:"remote,omitempty"`
	Tracker json.RawMessage `json:"tracker,omitempty"`
	Runs    []RunRec        `json:"runs,omitempty"`
}

// SlackChannel is a project's channel.
type SlackChannel struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	TeamID string `json:"team_id,omitempty"`
	URL    string `json:"url,omitempty"`
}

// RunRec records one supervised run.
type RunRec struct {
	Stage     string    `json:"stage"`
	StartedAt time.Time `json:"started_at"`
	Seconds   float64   `json:"seconds"`
	ExitCode  int       `json:"exit_code"`
	Reason    string    `json:"reason"`
	Log       string    `json:"log"`
	Attempts  int       `json:"attempts,omitempty"`
}

// Workspace is a provisioned task directory.
type Workspace struct {
	ID   string
	Dir  string
	Task Task
	// RepoPath overrides RepoDir for a single job, pointing at a per-job git
	// worktree instead of the shared sandbox clone. Runtime only and never
	// persisted: the workspace belongs to the repository, the worktree
	// belongs to one run, and conflating them would let a finished job's
	// path outlive it.
	RepoPath string
}

func (w Workspace) RepoDir() string {
	if w.RepoPath != "" {
		return w.RepoPath
	}
	return w.CloneDir()
}

// CloneDir is the shared sandbox clone, never a job worktree.
//
// Distinct from RepoDir, which a running job overrides to point at its own
// worktree. Anything that belongs to the REPOSITORY rather than to one run
// has to ask for this: git hooks are the case that proved it, since a
// worktree keeps none of its own and resolves them from the clone's common
// dir (OR-193).
func (w Workspace) CloneDir() string     { return filepath.Join(w.Dir, "repo") }
func (w Workspace) MetaDir() string      { return filepath.Join(w.Dir, ".orion") }
func (w Workspace) LogsDir() string      { return filepath.Join(w.MetaDir(), "logs") }
func (w Workspace) StateDir() string     { return filepath.Join(w.MetaDir(), "state") }
func (w Workspace) SettingsPath() string { return filepath.Join(w.MetaDir(), "settings.json") }
func (w Workspace) TaskPath() string     { return filepath.Join(w.MetaDir(), "task.json") }

// BuildCacheDir is the compiler cache shared by every job in this sandbox.
//
// Deliberately NOT under RepoDir: that resolves to a per-ticket worktree
// while a job is running, so a cache placed there would be built from cold
// once per ticket and thrown away with the worktree -- which is exactly the
// cost this exists to remove. Under Dir it is written once and warm for
// every ticket after the first.
//
// Not under RepoDir for a second reason: the sandbox clone is fast-forwarded
// between runs, and git refuses that on a dirty tree, so a multi-gigabyte
// cache in the clone would need its own ignore rule to avoid freezing the
// sandbox at the commit it was created from.
func (w Workspace) BuildCacheDir() string {
	return filepath.Join(w.MetaDir(), "cache", "go-build")
}

func (w Workspace) SandboxMode() string {
	if w.Task.Container {
		return "container (docker)"
	}
	return "os sandbox + permission denies"
}

// Directory and file modes for everything Orion owns.
//
// 0700 and 0600 rather than the usual 0755/0644, because this tree is not
// ordinary application state. It holds credentials, a spend ledger, learned
// lessons, and under projects/*/.orion/logs the FULL transcript of every
// supervised run: whatever source, error output or file content the agent
// read is in there verbatim. World-readable is the wrong default for all of
// it, and the transcripts are the part people would least expect to be
// readable by any other account on the machine.
const (
	HomeDirMode     = 0o700
	PrivateFileMode = 0o600
)

// EnsureHome creates ORION_HOME with private permissions, and tightens it if
// an earlier version left it open. Fixing in place matters: a directory
// created 0755 by a previous release stays 0755 forever otherwise.
func EnsureHome() (string, error) {
	h := Home()
	if err := os.MkdirAll(h, HomeDirMode); err != nil {
		return h, err
	}
	if fi, err := os.Stat(h); err == nil && fi.Mode().Perm()&0o077 != 0 {
		_ = os.Chmod(h, HomeDirMode)
	}
	return h, nil
}

// Home returns $ORION_HOME, defaulting to ~/.orion.
func Home() string {
	if h := strings.TrimSpace(os.Getenv("ORION_HOME")); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".orion"
	}
	return filepath.Join(home, ".orion")
}

func projectsDir() string { return filepath.Join(Home(), "projects") }

type NewOptions struct {
	Idea      string
	Template  string
	FromRepo  string
	Container bool
}

// New provisions a workspace. It is deliberately not idempotent: two
// identical ideas get two workspaces, because conflating them would let
// one task's failed state contaminate another's fresh attempt.
func New(opts NewOptions) (*Workspace, error) {
	idea := strings.TrimSpace(opts.Idea)
	if idea == "" {
		return nil, errors.New("an idea is required: orion new \"what you want built\"")
	}
	slug := Slugify(idea)
	id := slug + "-" + shortID()

	dir := filepath.Join(projectsDir(), id)
	if _, err := os.Stat(dir); err == nil {
		return nil, fmt.Errorf("workspace %s already exists", id)
	}

	if _, err := EnsureHome(); err != nil {
		return nil, err
	}
	for _, d := range []string{
		filepath.Join(dir, "repo"),
		filepath.Join(dir, "worktrees"),
		filepath.Join(dir, ".orion", "logs"),
		filepath.Join(dir, ".orion", "state"),
	} {
		if err := os.MkdirAll(d, HomeDirMode); err != nil {
			return nil, fmt.Errorf("provisioning %s: %w", d, err)
		}
	}

	ws := &Workspace{
		ID:  id,
		Dir: dir,
		Task: Task{
			ID: id, Idea: idea, Slug: slug,
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
			Stage: "intent", Status: "provisioned",
			Container: opts.Container, FromRepo: opts.FromRepo,
		},
	}

	if err := initRepo(ws, opts); err != nil {
		return nil, err
	}
	if err := writeSettings(ws); err != nil {
		return nil, err
	}
	if err := scaffoldChain(ws); err != nil {
		return nil, err
	}
	if err := ws.SaveTask(); err != nil {
		return nil, err
	}
	return ws, nil
}

// initRepo creates the git repository, cloning a source repo when one was
// given. Cloning uses --depth 1 by default: Orion needs the working tree,
// not the history, and a shallow clone keeps provisioning fast.
func initRepo(ws *Workspace, opts NewOptions) error {
	repo := ws.RepoDir()
	if opts.FromRepo != "" {
		cmd := exec.Command("git", "clone", "--depth", "1", opts.FromRepo, repo)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("cloning %s: %w\n%s", opts.FromRepo, err, out)
		}
		return nil
	}
	// -b requires git 2.28. Fall back to init plus a rename so older git
	// still produces the right branch name rather than "master".
	cmd := exec.Command("git", "init", "-q", "-b", "main", repo)
	if out, err := cmd.CombinedOutput(); err != nil {
		if out2, err2 := exec.Command("git", "init", "-q", repo).CombinedOutput(); err2 != nil {
			return fmt.Errorf("git init: %w\n%s%s", err, out, out2)
		}
		rename := exec.Command("git", "symbolic-ref", "HEAD", "refs/heads/main")
		rename.Dir = repo
		if out2, err2 := rename.CombinedOutput(); err2 != nil {
			return fmt.Errorf("setting initial branch to main: %w\n%s", err2, out2)
		}
	}
	// Establish the two long-lived branches immediately. Doing it at
	// provisioning time means no work can begin on the wrong branch, which
	// is the only reliable way to prevent a first commit landing on main.
	created, err := provision.InitBranches(repo, "main", "develop")
	if err != nil {
		return fmt.Errorf("establishing the branch model: %w", err)
	}
	ws.Task.Branches = created
	return nil
}

// Files under repo/ keep ordinary 0644. They are git-tracked content meant
// to be committed and read; git preserves only the executable bit, so a
// tighter mode would not travel anyway, and it would surprise anyone who
// later clones the workspace. The 0700 parent is what actually protects them.

// scaffoldChain lays down the artifact directories and a starting
// orion.json so the first stage has somewhere to write.
func scaffoldChain(ws *Workspace) error {
	repo := ws.RepoDir()
	for _, d := range []string{"intent", "specs", "plans", "evals"} {
		if err := os.MkdirAll(filepath.Join(repo, d), 0o755); err != nil {
			return err
		}
		// Keep empty directories in git so the chain's shape is visible
		// from the first commit.
		keep := filepath.Join(repo, d, ".gitkeep")
		if _, err := os.Stat(keep); os.IsNotExist(err) {
			if err := os.WriteFile(keep, nil, 0o644); err != nil {
				return err
			}
		}
	}
	cfgPath := filepath.Join(repo, "orion.json")
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		if err := os.WriteFile(cfgPath, []byte(defaultProjectConfig), 0o644); err != nil {
			return err
		}
	}
	gi := filepath.Join(repo, ".gitignore")
	if _, err := os.Stat(gi); os.IsNotExist(err) {
		if err := os.WriteFile(gi, []byte(".orion/\n"), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (w *Workspace) SaveTask() error {
	w.Task.UpdatedAt = time.Now().UTC()
	b, err := json.MarshalIndent(w.Task, "", "  ")
	if err != nil {
		return err
	}
	// Orion metadata: the idea, tracker binding and Slack channel. The
	// 0700 tree already protects it; matching modes keeps the line clear
	// between what is Orion's and what is the repository's.
	return os.WriteFile(w.TaskPath(), b, PrivateFileMode)
}

// IDs lists provisioned workspace ids. Separated from List so a caller that
// wants the data rather than the rendering does not have to parse a table.
func IDs() ([]string, error) {
	entries, err := os.ReadDir(projectsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// Open loads an existing workspace by id or by unambiguous prefix.
func Open(id string) (*Workspace, error) {
	dir, err := resolve(id)
	if err != nil {
		return nil, err
	}
	ws := &Workspace{ID: filepath.Base(dir), Dir: dir}
	b, err := os.ReadFile(ws.TaskPath())
	if err != nil {
		return nil, fmt.Errorf("workspace %s has no task.json: %w", ws.ID, err)
	}
	if err := json.Unmarshal(b, &ws.Task); err != nil {
		return nil, fmt.Errorf("workspace %s has a corrupt task.json: %w", ws.ID, err)
	}
	return ws, nil
}

// resolve accepts a full id or a prefix, refusing ambiguous prefixes
// rather than guessing which workspace was meant.
func resolve(id string) (string, error) {
	exact := filepath.Join(projectsDir(), id)
	if fi, err := os.Stat(exact); err == nil && fi.IsDir() {
		return exact, nil
	}
	entries, err := os.ReadDir(projectsDir())
	if err != nil {
		return "", fmt.Errorf("no workspaces yet (%s)", projectsDir())
	}
	var hits []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), id) {
			hits = append(hits, e.Name())
		}
	}
	switch len(hits) {
	case 0:
		return "", fmt.Errorf("no workspace matching %q", id)
	case 1:
		return filepath.Join(projectsDir(), hits[0]), nil
	default:
		return "", fmt.Errorf("%q is ambiguous: %s", id, strings.Join(hits, ", "))
	}
}

func List(w io.Writer) error {
	entries, err := os.ReadDir(projectsDir())
	if err != nil {
		fmt.Fprintf(w, "no workspaces yet. create one: orion new \"your idea\"\n")
		return nil
	}
	type row struct {
		id, stage, status, idea string
		updated                 time.Time
	}
	var rows []row
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		var t Task
		b, err := os.ReadFile(filepath.Join(projectsDir(), e.Name(), ".orion", "task.json"))
		if err != nil || json.Unmarshal(b, &t) != nil {
			rows = append(rows, row{id: e.Name(), stage: "?", status: "unreadable"})
			continue
		}
		rows = append(rows, row{e.Name(), t.Stage, t.Status, t.Idea, t.UpdatedAt})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].updated.After(rows[j].updated) })
	if len(rows) == 0 {
		fmt.Fprintf(w, "no workspaces yet. create one: orion new \"your idea\"\n")
		return nil
	}
	fmt.Fprintf(w, "%-34s %-10s %-12s %s\n", "ID", "STAGE", "STATUS", "IDEA")
	for _, r := range rows {
		fmt.Fprintf(w, "%-34s %-10s %-12s %s\n", r.id, r.stage, r.status, truncate(r.idea, 50))
	}
	return nil
}

func PrintPath(w io.Writer, id string) error {
	dir, err := resolve(id)
	if err != nil {
		return err
	}
	fmt.Fprintln(w, filepath.Join(dir, "repo"))
	return nil
}

func Status(w io.Writer, id string) error {
	ws, err := Open(id)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "id       %s\nidea     %s\nstage    %s\nstatus   %s\nsandbox  %s\nrepo     %s\n",
		ws.ID, ws.Task.Idea, ws.Task.Stage, ws.Task.Status, ws.SandboxMode(), ws.RepoDir())
	if n := len(ws.Task.Runs); n > 0 {
		last := ws.Task.Runs[n-1]
		fmt.Fprintf(w, "last run %s exit=%d %s (%.0fs)\n         %s\n",
			last.Stage, last.ExitCode, last.Reason, last.Seconds, last.Log)
	}
	// A tripped breaker is the single most useful thing to surface here:
	// it is why a workspace that looks idle is actually waiting on a human.
	if b, err := os.ReadFile(filepath.Join(ws.StateDir(), "tripped")); err == nil {
		fmt.Fprintf(w, "BREAKER  %s\n", strings.TrimSpace(string(b)))
	}
	return nil
}

// Remove deletes a workspace. Requires force when the task is not done,
// because an in-flight workspace holds the only copy of its work.
func Remove(id string, force bool) error {
	ws, err := Open(id)
	if err != nil {
		return err
	}
	if !force && ws.Task.Status != "done" && ws.Task.Status != "abandoned" {
		return fmt.Errorf("workspace %s is %s, not finished.\n"+
			"  Its repo at %s holds the only copy of this work.\n"+
			"  Re-run with --force if you are sure.",
			ws.ID, ws.Task.Status, ws.RepoDir())
	}
	return os.RemoveAll(ws.Dir)
}

// Slugify produces a short, filesystem-safe stem from free text.
func Slugify(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	words := strings.Split(out, "-")
	if len(words) > 5 {
		words = words[:5]
	}
	out = strings.Join(words, "-")
	if out == "" {
		out = "task"
	}
	if len(out) > 40 {
		out = strings.Trim(out[:40], "-")
	}
	return out
}

func shortID() string {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%06x", time.Now().UnixNano()&0xffffff)
	}
	return hex.EncodeToString(b)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
