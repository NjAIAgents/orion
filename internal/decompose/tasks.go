// Package decompose turns a decomposed task list into tracker items.
//
// Orion delegates decomposition to a skill today: the decompose stage prompt
// tells the agent to run /pm-plan, which does two jobs -- break a plan into a
// tree, then create that tree. Adopting spec-kit's /speckit.tasks replaces
// the first job with a stronger artifact than a skill invents on the spot:
// phased, with [P] parallel markers, story groups and exact file paths. What
// is left is turning that artifact into tracker items, which is a
// tracker-client problem rather than a skill-shaped one -- and Orion already
// owns every other piece of the contract (the client, the transitions, the
// label state machine, and the routing table `orion routes` publishes).
//
// WHAT THIS IS NOT: it is not a replacement for the delegated path. The
// decompose stage prompt is untouched, so a project with no spec-kit output
// decomposes exactly as it did before. This is a second, operator-invoked
// route for the projects that DO have a tasks.md -- see docs/decisions/0001,
// which is also why nothing here decides whether a later stage runs.
//
// SCOPE LIMIT, STATED RATHER THAN HIDDEN: the tracker seam OR-303 describes
// -- an interface over the Jira client with Linear, Notion and GitHub
// backends behind it -- has not landed. Everything above Backend below is
// written against that interface and knows nothing about Jira; the only
// implementation shipped is jira.go. Per OR-302's sequencing clause that
// makes this a Jira-only capability, which is why it is opt-in and why
// /pm-plan remains the default and fully available path for a project on any
// other tracker.
package decompose

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// Kind is the level of an item in the tree.
//
// Neutral on purpose. Jira calls the bottom level a Sub-task when it hangs
// off a Story and a Task when it hangs off an Epic; GitHub has no Epic at
// all. Naming the levels after the SHAPE rather than after one tracker's
// vocabulary is what lets the mapping live in the backend, where the
// difference actually is.
type Kind string

const (
	KindEpic  Kind = "epic"
	KindStory Kind = "story"
	KindTask  Kind = "task"
)

// Item is one node of the neutral tree.
type Item struct {
	// ID is the marker the source artifact used: T001 for a task, US1 for a
	// story, empty for the epic. Kept because the artifact's own dependency
	// lines refer to tasks by it ("T005 depends on T004"), so dropping it
	// would break every cross-reference in the descriptions.
	ID   string
	Kind Kind
	// Summary is what the tracker shows in a list. It LEADS WITH ID for the
	// same reason, and it is the reconcile identity: see Existing.
	Summary string
	Body    string
	Labels  []string
	// Paths are the exact file paths the task line named. Extracted rather
	// than merely left in the prose because they are the strongest
	// structural signal about a task that a machine can read -- the routing
	// marker is chosen from them (see marker.go).
	Paths []string
	// Parallel records the [P] marker: this task may run alongside the other
	// [P] tasks in its phase.
	Parallel bool
	Phase    string
	Children []*Item
}

// Tree is one tasks.md, parsed.
type Tree struct {
	// Slug scopes reconciliation. Task IDs restart at T001 in every
	// tasks.md, so "is T001 already in the tracker?" is only answerable
	// within one feature -- the slug is what makes the question well-formed
	// across a project with several features decomposed into it.
	Slug string
	Epic *Item
	// Source is the file this came from, for the preview and the epic body.
	Source string
}

// Label is the identity label every item created from this tree carries.
// It is how a re-run finds what the last run made.
func (t *Tree) Label() string { return "orion-spec-" + t.Slug }

// Walk visits every item parent-first, depth-first: the epic, then each
// story, then that story's tasks.
//
// The order is the creation order and it is not incidental. A child cannot
// be created before the parent whose key it needs, and a run that fails
// partway has to be resumable -- which is only true if the order is the same
// on every run.
func (t *Tree) Walk(fn func(it, parent *Item) error) error {
	if t == nil || t.Epic == nil {
		return nil
	}
	return walk(t.Epic, nil, fn)
}

func walk(it, parent *Item, fn func(it, parent *Item) error) error {
	if err := fn(it, parent); err != nil {
		return err
	}
	for _, c := range it.Children {
		if err := walk(c, it, fn); err != nil {
			return err
		}
	}
	return nil
}

// Count reports how many items the tree holds, the epic included.
func (t *Tree) Count() int {
	n := 0
	_ = t.Walk(func(*Item, *Item) error { n++; return nil })
	return n
}

var (
	// taskLine matches the format /speckit.tasks documents for itself:
	// `[ID] [P?] [Story] Description`, written as a markdown checkbox. The
	// checkbox and the T-id are both required, which is what keeps the
	// template's own "## Format" section -- whose bullets look like
	// `- **[P]**: Can run in parallel` -- out of the tree.
	taskLine = regexp.MustCompile(`^\s*[-*]\s*\[[ xX]\]\s*\*{0,2}(T\d+)\*{0,2}\s*(.*)$`)
	// storyTag is the [USn] group marker, and parallelTag the [P] marker.
	// Matched anywhere in the remainder rather than only at the front:
	// real output writes them in either order, and one template revision
	// putting [P] second is not a reason to lose the whole task.
	storyTag    = regexp.MustCompile(`\[US(\d+)\]`)
	parallelTag = regexp.MustCompile(`\[P\]`)
	// phaseStory recognises the heading that NAMES a story group, e.g.
	// "## Phase 3: User Story 2 - Checkout (Priority: P2)".
	phaseStory = regexp.MustCompile(`(?i)user story\s*(\d+)\s*[-:–]?\s*(.*)$`)
	priorityIn = regexp.MustCompile(`\s*\((?i:priority)[^)]*\)\s*`)
	// pathish is a file path as a task line writes one: at least one
	// separator and an extension.
	pathish = regexp.MustCompile("`?\\b([\\w.@-]+/[\\w./@-]*[\\w-]+\\.[A-Za-z]\\w*)`?")
	// bareManifest is the other half: a file a task line names with no
	// directory at all, because it only ever sits at the repository root.
	// "Initialise the module with dependencies in go.mod" is a real
	// /speckit.tasks Setup line, and pathish cannot see it -- it has no
	// separator.
	//
	// AN EXPLICIT LIST, not a general word.extension pattern. A general one
	// would extract "e.g" from prose and "sight" from "in sight.", and a
	// wrong path in a description is worse than a missing one: the paths are
	// what a later reader treats as the exact files to change. Longest
	// alternatives first, so package-lock.json is not read as package.json.
	bareManifest = regexp.MustCompile(`\b(go\.mod|go\.sum|package-lock\.json|package\.json|pnpm-lock\.yaml|yarn\.lock|Cargo\.toml|Cargo\.lock|pyproject\.toml|requirements\.txt|setup\.py|Gemfile\.lock|Gemfile|composer\.json|build\.gradle|pom\.xml|tsconfig\.json|Dockerfile|Makefile)\b`)
	goalLine     = regexp.MustCompile(`^\s*\*{0,2}Goal\*{0,2}\s*:\s*(.*)$`)
)

// Parse reads a /speckit.tasks tasks.md into the neutral tree.
//
// source is the path the text came from; it is recorded on the tree and
// named in the epic body so an item's origin is discoverable from the
// tracker rather than only from the machine that ran this.
func Parse(text, source string) (*Tree, error) {
	lines := strings.Split(text, "\n")

	tree := &Tree{Source: source}
	epic := &Item{Kind: KindEpic}

	var (
		phase       string
		phaseStoryN string
		storyByNum  = map[string]*Item{}
		storyOrder  []string
		storyGoal   = map[string]string{}
		storyTitle  = map[string]string{}
		phases      []string
		depends     []string
		inDepends   bool
	)

	for _, raw := range lines {
		line := strings.TrimRight(raw, " \t\r")

		if h, ok := heading(line, "# "); ok {
			epic.Summary = strings.TrimSpace(strings.TrimPrefix(h, "Tasks:"))
			continue
		}
		if h, ok := heading(line, "## "); ok {
			phase = h
			phaseStoryN = ""
			// A "Dependencies" section is the artifact's statement of
			// ORDER. It belongs on the epic verbatim: it names tasks by id,
			// so re-deriving it per item would either duplicate it or lose
			// the cross-references it is made of.
			inDepends = strings.HasPrefix(strings.ToLower(h), "dependencies")
			if m := phaseStory.FindStringSubmatch(h); m != nil {
				phaseStoryN = m[1]
				storyTitle[m[1]] = cleanTitle(m[2])
			} else if strings.HasPrefix(strings.ToLower(h), "phase") {
				phases = append(phases, h)
			}
			continue
		}
		if _, ok := heading(line, "### "); ok {
			continue
		}
		if inDepends {
			if strings.TrimSpace(line) != "" {
				depends = append(depends, line)
			}
			continue
		}
		if phaseStoryN != "" {
			if m := goalLine.FindStringSubmatch(line); m != nil {
				storyGoal[phaseStoryN] = strings.TrimSpace(m[1])
			}
		}

		m := taskLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		id, rest := m[1], m[2]

		parallel := parallelTag.MatchString(rest)
		rest = parallelTag.ReplaceAllString(rest, "")

		num := ""
		if s := storyTag.FindStringSubmatch(rest); s != nil {
			num = s[1]
			rest = storyTag.ReplaceAllString(rest, "")
		} else if phaseStoryN != "" {
			// The phase heading already said which story this is. Trusting
			// it means a task line that omits the redundant tag still lands
			// under its story instead of being orphaned to the epic.
			num = phaseStoryN
		}

		desc := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(rest), "-–:"))
		task := &Item{
			ID:       id,
			Kind:     KindTask,
			Summary:  strings.TrimSpace(id + " " + desc),
			Paths:    pathsIn(desc),
			Parallel: parallel,
			Phase:    phase,
		}
		task.Body = taskBody(task, desc)

		if num == "" {
			// A task in no story group -- Setup, Foundational, Polish -- is
			// a child of the EPIC, not of an invented story. Its own phase
			// is already recorded, and manufacturing a "Misc" story to hold
			// it would put a container in the tracker that the source
			// artifact never described.
			epic.Children = append(epic.Children, task)
			continue
		}
		st, ok := storyByNum[num]
		if !ok {
			st = &Item{ID: "US" + num, Kind: KindStory, Phase: phase}
			storyByNum[num] = st
			storyOrder = append(storyOrder, num)
		}
		st.Children = append(st.Children, task)
	}

	if epic.Summary == "" {
		// No `# Tasks:` heading. The directory a /speckit.tasks file sits in
		// is named for the feature, so it is the better fallback than the
		// literal word "tasks".
		epic.Summary = fallbackName(source)
	}
	if epic.Summary == "" {
		return nil, fmt.Errorf("%s names no feature: expected a `# Tasks: <name>` heading", source)
	}

	// Stories in first-appearance order, and ahead of the ungrouped tasks:
	// the tracker shows children in the order they were created, and the
	// story groups are the point of the artifact.
	stories := make([]*Item, 0, len(storyOrder))
	for _, num := range storyOrder {
		st := storyByNum[num]
		title := storyTitle[num]
		if title == "" {
			title = "User Story " + num
		}
		st.Summary = st.ID + " " + title
		st.Body = storyBody(st, storyGoal[num])
		stories = append(stories, st)
	}
	epic.Children = append(stories, epic.Children...)

	tree.Slug = slug(epic.Summary)
	tree.Epic = epic
	epic.Body = epicBody(tree, phases, depends)

	label(tree)
	return tree, nil
}

func heading(line, prefix string) (string, bool) {
	if !strings.HasPrefix(line, prefix) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(line, prefix)), true
}

// cleanTitle strips the decoration a phase heading carries around the story
// name: the priority parenthetical, and trailing marker text like "MVP".
func cleanTitle(s string) string {
	s = priorityIn.ReplaceAllString(s, " ")
	s = strings.Map(func(r rune) rune {
		if r > 0x2000 { // emoji and other decoration
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(strings.TrimSpace(strings.TrimSuffix(s, "MVP")), "-")
	return strings.TrimSpace(s)
}

func pathsIn(desc string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		p = strings.Trim(p, "`,.")
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}

	for _, m := range pathish.FindAllStringSubmatch(desc, -1) {
		add(m[1])
	}
	// Bare manifests are looked for in what is LEFT once the qualified paths
	// are taken out. web/package.json is one path, and scanning the whole
	// line again would report package.json beside it as a second file that
	// the task never mentioned.
	for _, m := range bareManifest.FindAllString(pathish.ReplaceAllString(desc, " "), -1) {
		add(m)
	}
	return out
}

func taskBody(t *Item, desc string) string {
	var b strings.Builder
	b.WriteString(desc)
	b.WriteString("\n\n")
	if t.Phase != "" {
		fmt.Fprintf(&b, "Phase: %s\n", t.Phase)
	}
	// The [P] marker is the reason this artifact beats a flat list, so it is
	// written out in words rather than left as a bracket a reader has to
	// know the template to decode.
	if t.Parallel {
		b.WriteString("Parallel: [P] -- may run alongside the other [P] tasks in this phase.\n")
	} else {
		b.WriteString("Parallel: no -- run this in phase order.\n")
	}
	if len(t.Paths) > 0 {
		fmt.Fprintf(&b, "Files: %s\n", strings.Join(t.Paths, ", "))
	}
	return b.String()
}

func storyBody(st *Item, goal string) string {
	var b strings.Builder
	if goal != "" {
		b.WriteString(goal)
		b.WriteString("\n\n")
	}
	if st.Phase != "" {
		fmt.Fprintf(&b, "Phase: %s\n", st.Phase)
	}
	fmt.Fprintf(&b, "Tasks: %d\n", len(st.Children))
	return b.String()
}

func epicBody(t *Tree, phases, depends []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Decomposed from %s by `orion decompose`.\n", t.Source)
	if len(phases) > 0 {
		b.WriteString("\nPhases:\n")
		for _, p := range phases {
			fmt.Fprintf(&b, "- %s\n", p)
		}
	}
	if len(depends) > 0 {
		b.WriteString("\nDependencies and execution order, as the source stated them:\n")
		for _, d := range depends {
			fmt.Fprintf(&b, "%s\n", d)
		}
	}
	return b.String()
}

// fallbackName reads the feature name off the path a /speckit.tasks file
// lives at: specs/003-user-auth/tasks.md -> "user auth".
func fallbackName(source string) string {
	dir := path.Base(path.Dir(strings.ReplaceAll(source, `\`, "/")))
	if dir == "." || dir == "/" || dir == "" {
		return ""
	}
	dir = strings.TrimLeft(dir, "0123456789-")
	return strings.TrimSpace(strings.ReplaceAll(dir, "-", " "))
}

var notSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slug(name string) string {
	s := notSlug.ReplaceAllString(strings.ToLower(name), "-")
	s = strings.Trim(s, "-")
	// Jira caps a label's length well above this; the cap is here so the
	// label stays readable on a board rather than to satisfy an API.
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	return s
}
