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
// decomposes exactly as it did before. This is the native route for the
// projects that DO have a tasks.md: `orion plan` runs it as the decompose
// step when <FeatureDir>/tasks.md exists, stamping the queue label at the
// levels Queue documents, and falls back to the supervised stage otherwise;
// `orion decompose` runs the same code by hand -- see docs/decisions/0001,
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
	"strconv"
	"strings"
	"unicode"

	"github.com/orion-sdlc/orion/internal/fanout"
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
	// Human records the [HUMAN] marker: work no agent can do -- a credential
	// a person holds, a console click, a conversation with another team.
	// The queue label is withheld from these (OR-414).
	Human bool
	// DoneWhen is the exit condition: what a reviewer checks to say this is
	// finished. Stated by the artifact when the orion preset asked for it
	// (OR-411), derived from what the line already names when it did not
	// (OR-412) -- and Derived says which, because a reader weighs the two
	// differently.
	DoneWhen string
	Derived  bool
	// Criteria are a story's acceptance criteria, in the artifact's own
	// words. A story with none says so rather than implying they were
	// considered.
	Criteria []string
	Phase    string
	Children []*Item

	// rawDesc is the task line's full text, kept until the body is written
	// after the file has been read.
	rawDesc string
}

// Coupling is two sibling items that declared the same ground (OR-260).
//
// A tracker tree whose siblings collide is a tree that will batch badly for
// its whole life: the queue will refuse to admit them together, pass after
// pass, and nothing downstream can undo a decomposition. So it is said HERE,
// while the shape of the work is still in view and the tree has not been
// created yet.
type Coupling struct {
	A, B   string   // the two summaries, in tree order
	Shared []string // the ground they both declared
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
	// Coupled are the sibling pairs whose declared scopes overlap. Reported,
	// never resolved: see couplings.
	Coupled []Coupling
	// Blocks are the ordering edges the Dependencies section states, as
	// task ids: Blocks[i] says "Blocker must finish before Blocked starts".
	// Prose until now, pasted into the epic body, which meant the queue saw
	// none of it and admitted every task at once (docs/decisions/0023).
	Blocks []Edge
}

// Edge is one ordering statement: Blocker must finish before Blocked can
// start. Ids are the artifact's own (T012, T041), resolved to tracker keys
// at creation.
type Edge struct {
	Blocker string
	Blocked string
	// Why is the line the edge was read from, so a person looking at a link
	// in the tracker can find the sentence that put it there.
	Why string
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
	// The id is T then digits, and may carry a letter suffix: a task
	// inserted between two others is T004a, not a renumbering of everything
	// after it. Matching digits alone split "T004a" into id T004 and a
	// description opening with a stray "a" -- two tasks then shared one id,
	// and the tree carried a duplicate and a ticket titled "a Run the
	// reconciliation spike" (FOUND ON A REAL PROJECT).
	taskLine = regexp.MustCompile(`^\s*[-*]\s*\[[ xX]\]\s*\*{0,2}(T\d+[a-zA-Z]?)\*{0,2}\s+(.*)$`)
	// storyTag is the [USn] group marker, and parallelTag the [P] marker.
	// Matched anywhere in the remainder rather than only at the front:
	// real output writes them in either order, and one template revision
	// putting [P] second is not a reason to lose the whole task.
	storyTag    = regexp.MustCompile(`\[US(\d+)\]`)
	parallelTag = regexp.MustCompile(`\[P\]`)
	// phaseStory recognises the heading that NAMES a story group, e.g.
	// "## Phase 3: User Story 2 - Checkout (Priority: P2)".
	phaseStory = regexp.MustCompile(`(?i)user story\s*(\d+)\s*[-:–—]?\s*(.*)$`)
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
	// doneWhenLine is the exit condition the orion preset asks for under
	// every task line. Indented, because it belongs to the task above it.
	doneWhenLine = regexp.MustCompile(`^\s+\*{0,2}Done when\*{0,2}\s*:\s*(.*)$`)
	// criteriaHead opens a story phase's acceptance-criteria block; the
	// numbered or bulleted lines under it are the criteria.
	criteriaHead = regexp.MustCompile(`(?i)^\s*\*{0,2}Acceptance criteria\*{0,2}\b`)
	criteriaItem = regexp.MustCompile(`^\s*(?:\d+\.|[-*+])\s+(.*)$`)
	// humanTag marks work no agent can do.
	humanTag = regexp.MustCompile(`\[HUMAN\]`)
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
		// defer0 are the tasks whose bodies are written after the file is
		// read, because a task's own "Done when:" line comes after it.
		defer0 []*Item
		// lastTask is the task a following "Done when:" line belongs to,
		// and inCriteria whether the lines being read are a story's
		// acceptance criteria. Both are cleared by the next heading or
		// task line, so a stray line never attaches to something distant.
		lastTask      *Item
		inCriteria    bool
		storyCriteria = map[string][]string{}
	)

	for _, raw := range lines {
		line := strings.TrimRight(raw, " \t\r")

		if h, ok := heading(line, "# "); ok {
			// The FIRST one names the feature, and only if it is the
			// `# Tasks: <name>` heading the format documents. A later `# `
			// line is a section of the document, not a second name --
			// FOUND ON A REAL PROJECT: a "# Dependencies" section made the
			// epic "Then T047, then T048.", which also became the identity
			// label a re-run reconciles by.
			if epic.Summary == "" && strings.HasPrefix(h, "Tasks:") {
				epic.Summary = strings.TrimSpace(strings.TrimPrefix(h, "Tasks:"))
			}
			continue
		}
		if h, ok := heading(line, "## "); ok {
			phase = h
			phaseStoryN = ""
			lastTask, inCriteria = nil, false
			// A "Dependencies" section is the artifact's statement of
			// ORDER. It belongs on the epic verbatim: it names tasks by id,
			// so re-deriving it per item would either duplicate it or lose
			// the cross-references it is made of.
			inDepends = strings.HasPrefix(strings.ToLower(h), "dependencies")
			// A story is named by its PHASE heading and by nothing else.
			// "## Parallel example: User Story 2" is documentation about a
			// story, not the story -- and it captured an empty title that
			// overwrote the real one, so the story was created as
			// "User Story 2" (FOUND ON A REAL PROJECT). A later heading
			// never blanks a name that is already known, either.
			if m := phaseStory.FindStringSubmatch(h); m != nil && strings.HasPrefix(strings.ToLower(h), "phase") {
				phaseStoryN = m[1]
				if title := cleanTitle(m[2]); title != "" {
					storyTitle[m[1]] = title
				}
			} else if strings.HasPrefix(strings.ToLower(h), "phase") {
				phases = append(phases, h)
			}
			continue
		}
		if _, ok := heading(line, "### "); ok {
			lastTask, inCriteria = nil, false
			// Inside the Dependencies section a sub-heading is part of what
			// the section says -- "### Parallel opportunities" changes the
			// meaning of every bullet under it -- so it is carried with the
			// body rather than swallowed here.
			if inDepends {
				depends = append(depends, line)
			}
			continue
		}
		if inDepends {
			if strings.TrimSpace(line) != "" {
				depends = append(depends, line)
			}
			continue
		}
		// A "Done when:" line belongs to the task above it: the exit
		// condition the orion preset asks for (OR-411).
		if lastTask != nil {
			if m := doneWhenLine.FindStringSubmatch(line); m != nil {
				lastTask.DoneWhen = strings.TrimSpace(m[1])
				continue
			}
		}
		// A story phase's acceptance-criteria block: the heading opens it,
		// the numbered or bulleted lines under it are the criteria, and a
		// blank line closes it.
		if phaseStoryN != "" && criteriaHead.MatchString(line) {
			inCriteria = true
			continue
		}
		// A TASK LINE ENDS THE BLOCK, and is never a criterion. Both open
		// with "- ", so a criteria block still open when the phase's first
		// task arrived absorbed it -- three tasks silently lost, each the
		// first after a block (FOUND ON A REAL PROJECT).
		if inCriteria && taskLine.MatchString(line) {
			inCriteria = false
		}
		if inCriteria {
			// A BLANK LINE DOES NOT CLOSE THE BLOCK. Real output writes the
			// heading, then a blank line, then the numbered criteria -- so
			// closing on the first blank read none of them at all (FOUND ON
			// A REAL PROJECT: six blocks written, zero parsed). What closes
			// it is the next thing that is plainly not a criterion: a task
			// line, a heading, or ordinary prose. Blank lines inside are
			// skipped, and a blank line before the first item is expected.
			if strings.TrimSpace(line) == "" {
				continue
			}
			if m := criteriaItem.FindStringSubmatch(line); m != nil {
				if c := strings.TrimSpace(m[1]); c != "" {
					storyCriteria[phaseStoryN] = append(storyCriteria[phaseStoryN], c)
				}
				continue
			}
			// A continuation of the criterion above it: indented, and not a
			// line that belongs to something else. A task line and its
			// "Done when:" are both indented too, and folding one of those
			// into a criterion swallows the task with it.
			n := len(storyCriteria[phaseStoryN])
			isTask := taskLine.MatchString(line) || doneWhenLine.MatchString(line)
			if n > 0 && !isTask && (strings.HasPrefix(line, "   ") || strings.HasPrefix(line, "\t")) {
				storyCriteria[phaseStoryN][n-1] += " " + strings.TrimSpace(line)
				continue
			}
			inCriteria = false
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
		human := humanTag.MatchString(rest)
		rest = humanTag.ReplaceAllString(rest, "")

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
			Summary:  strings.TrimSpace(id + " " + summarise(desc)),
			Paths:    pathsIn(desc),
			Parallel: parallel,
			Human:    human,
			Phase:    phase,
			rawDesc:  desc,
		}
		lastTask = task
		defer0 = append(defer0, task)

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

	// Every task's body, now that its "Done when:" line (which follows it)
	// has been read. A task the artifact left without one gets a derived
	// condition from what its own line already names -- the files it
	// declares and the requirements it cites -- marked as derived, because
	// a reader weighs a stated condition and an inferred one differently
	// (OR-412).
	for _, t := range defer0 {
		if t.DoneWhen == "" {
			t.DoneWhen, t.Derived = deriveDoneWhen(t), true
		}
		t.Body = taskBody(t, t.rawDesc)
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
		st.Criteria = storyCriteria[num]
		// The union of its tasks', because a STORY is the unit an agent
		// claims and works in one branch (internal/tracker/children.go) --
		// so the story is the level a batch collides at, and therefore the
		// level a declared scope has to exist at.
		st.Paths = declaredPaths(st)
		stories = append(stories, st)
	}
	epic.Children = append(stories, epic.Children...)

	tree.Slug = slug(epic.Summary)
	tree.Epic = epic
	// Coupling is found before the bodies are written, because it is written
	// INTO them: a story that will collide with a sibling says so on its own
	// record, where whoever picks it up will read it.
	tree.Coupled = couplings(stories)
	for _, num := range storyOrder {
		st := storyByNum[num]
		st.Body = storyBody(st, storyGoal[num], tree.Coupled)
	}
	tree.Blocks = dependencyEdges(depends, tree)
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
	// Emoji and symbols go; PUNCTUATION STAYS. Dropping everything above
	// U+2000 took the em dash and the en dash with it -- and a heading
	// written "User Story 2 — See AWS cost by account" lost its title
	// entirely, because the dash led the capture and its removal left a
	// leading space that the marker trims below could not see past
	// (FOUND ON A REAL PROJECT: the story was created as "User Story 2").
	s = strings.Map(func(r rune) rune {
		switch {
		case r < 0x2000:
			return r
		case unicode.IsPunct(r) || unicode.IsSpace(r):
			return r
		}
		return -1
	}, s)
	s = strings.TrimSpace(s)
	// A leading separator the regex left behind, now that it survives.
	s = strings.TrimSpace(strings.TrimLeft(s, "-–—:"))
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

// deriveDoneWhen states an exit condition from what the task line already
// names, for an artifact written without one.
//
// It infers nothing about behaviour: the files the line declares and the
// requirement ids it cites are facts on the line, and "those files exist,
// are committed, and the requirement they cite is demonstrable" is the
// weakest honest condition. Weak on purpose -- a derived condition that
// overstated what was checked would be worse than none, because a reviewer
// would trust it.
func deriveDoneWhen(t *Item) string {
	var parts []string
	if len(t.Paths) > 0 {
		parts = append(parts, fmt.Sprintf("%s exist and are committed", strings.Join(t.Paths, ", ")))
	}
	if refs := requirementRefs(t.rawDesc); len(refs) > 0 {
		parts = append(parts, fmt.Sprintf("the behaviour %s describes is demonstrable", strings.Join(refs, ", ")))
	}
	if len(parts) == 0 {
		// Nothing on the line to hang a condition on. Say that, rather than
		// inventing one: an item whose done-when is a guess is the failure
		// this whole mechanism exists to remove.
		return "NOT STATED. The task list gave no exit condition and the line names no file " +
			"and no requirement to derive one from. Agree what \"done\" means before starting."
	}
	return strings.Join(parts, ", and ")
}

// requirementRe finds the requirement and criterion ids a task line cites:
// FR-011, NFR-S8, SC-004, and the research references beside them.
var requirementRe = regexp.MustCompile(`\b((?:FR|NFR|SC|OQ)-[A-Z]?\d+)\b`)

func requirementRefs(desc string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range requirementRe.FindAllStringSubmatch(desc, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

func taskBody(t *Item, desc string) string {
	var b strings.Builder
	b.WriteString(desc)
	b.WriteString("\n\n")
	// The exit condition first among the facts: it is what the person or
	// agent working this item is held to, and the one line a reviewer
	// checks. Said to be derived when it is, so nobody reads an inference
	// as an agreement.
	if t.DoneWhen != "" {
		if t.Derived {
			b.WriteString("Done when (derived by Orion from this line -- the task list stated none):\n  ")
		} else {
			b.WriteString("Done when:\n  ")
		}
		b.WriteString(t.DoneWhen)
		b.WriteString("\n\n")
	}
	if t.Human {
		b.WriteString("HUMAN: the task list marks this as work no agent can do, so Orion does not " +
			"offer it to the queue. A person picks it up.\n")
	}
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

func storyBody(st *Item, goal string, coupled []Coupling) string {
	var b strings.Builder
	if goal != "" {
		b.WriteString(goal)
		b.WriteString("\n\n")
	}
	if st.Phase != "" {
		fmt.Fprintf(&b, "Phase: %s\n", st.Phase)
	}
	fmt.Fprintf(&b, "Tasks: %d\n", len(st.Children))
	// Acceptance criteria, in the artifact's own words. A story without
	// them says so: a body that simply omitted the section would read as
	// though none were needed.
	if len(st.Criteria) > 0 {
		b.WriteString("\nAcceptance criteria:\n")
		for _, c := range st.Criteria {
			fmt.Fprintf(&b, "  - %s\n", c)
		}
	} else {
		b.WriteString("\nAcceptance criteria: NONE STATED in the task list. " +
			"The spec's Acceptance Scenarios for this story are the agreed text; " +
			"agree what this story must satisfy before working it.\n")
	}
	// The declared scope, in the one spelling the queue manager reads back
	// (internal/tracker/scope.go). A story with no file paths in any of its
	// tasks writes no line at all rather than an empty one: absent means
	// unknown, and unknown must never read as "touches nothing".
	if len(st.Paths) > 0 {
		fmt.Fprintf(&b, "Files: %s\n", strings.Join(st.Paths, ", "))
	}
	for _, c := range coupled {
		other := ""
		switch st.Summary {
		case c.A:
			other = c.B
		case c.B:
			other = c.A
		default:
			continue
		}
		fmt.Fprintf(&b, "Coupled with %q: both declare %s. "+
			"These two will not be admitted to one batch; merge them, or order them "+
			"with a blocking link, before treating them as independent.\n",
			other, strings.Join(c.Shared, ", "))
	}
	return b.String()
}

// declaredPaths is the union of an item's own paths and its children's, in
// first-appearance order.
func declaredPaths(it *Item) []string {
	var out []string
	seen := map[string]bool{}
	add := func(ps []string) {
		for _, p := range ps {
			if p != "" && !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	add(it.Paths)
	for _, c := range it.Children {
		add(declaredPaths(c))
	}
	return out
}

// couplings reports every pair of siblings whose declared scopes overlap.
//
// IT REPORTS AND DOES NOT RESOLVE. The issue that asked for this named three
// answers to a collision -- merge the two items, sequence them with a real
// blocking link, or state plainly that they are coupled -- and only the third
// is one a parser can give honestly. Merging changes what the source artifact
// said the work was; sequencing has to decide WHICH of the two goes first,
// which is a judgement about the work and not about the text. So this says it,
// on both records and in the preview, before a human approves the tree.
//
// Through fanout.Overlap, the same implementation the queue admits on, so what
// planning warns about and what the queue later refuses cannot disagree.
func couplings(items []*Item) []Coupling {
	var out []Coupling
	for i, a := range items {
		for _, b := range items[i+1:] {
			shared := fanout.Overlap(
				fanout.Scope{Key: a.Summary, Paths: a.Paths},
				fanout.Scope{Key: b.Summary, Paths: b.Paths})
			if len(shared) == 0 {
				continue
			}
			out = append(out, Coupling{A: a.Summary, B: b.Summary, Shared: shared})
		}
	}
	return out
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

// summaryMax is how long a tracker summary may be.
//
// A /speckit.tasks line is a paragraph: the file paths, the section to fill,
// the research references, the conditions. All of that belongs in the body,
// where whoever works the ticket reads it -- and none of it belongs in the
// title, which is what a person scans a backlog by and what every list, board
// and notification shows. FOUND ON A REAL PROJECT: a 94-item preview no one
// could read, and Jira titles of four hundred characters.
const summaryMax = 100

// summarise is the first sentence of a task description, bounded.
//
// The first sentence is the imperative: "Write internal/cost/reconcile.go",
// "Request read-only payer-account access". What follows it is detail the
// body carries in full.
func summarise(desc string) string {
	s := strings.Join(strings.Fields(desc), " ")
	// The first sentence, when one ends early enough to be a title. A full
	// stop inside a path or a version is not a sentence end, so the break
	// has to be followed by a space.
	if i := strings.Index(s, ". "); i > 0 && i < summaryMax {
		return strings.TrimSpace(s[:i])
	}
	// Otherwise the first clause, at a semicolon or an em dash.
	for _, sep := range []string{"; ", " -- ", " — "} {
		if i := strings.Index(s, sep); i > 0 && i < summaryMax {
			return strings.TrimSpace(s[:i])
		}
	}
	if len(s) <= summaryMax {
		return strings.TrimSuffix(s, ".")
	}
	cut := s[:summaryMax]
	if i := strings.LastIndexByte(cut, ' '); i > summaryMax/2 {
		cut = cut[:i]
	}
	cut = closeBrackets(cut)
	return strings.TrimRight(cut, " ,;:-") + "…"
}

// closeBrackets drops a trailing fragment left inside an opener the cut did
// not reach the close of.
//
// "…run-rate (linear" reads as a broken sentence rather than a shortened
// one, and the words after the bracket are the qualifier, never the point
// (OR-415). Cutting back to the opener loses nothing a reader wanted.
func closeBrackets(s string) string {
	for _, pair := range []struct{ open, close byte }{{'(', ')'}, {'[', ']'}, {'{', '}'}} {
		i := strings.LastIndexByte(s, pair.open)
		if i >= 0 && strings.IndexByte(s[i:], pair.close) < 0 {
			s = strings.TrimRight(s[:i], " ,;:-")
		}
	}
	return s
}

// afterTask reads "after T012" / "after T000" from a dependency line.
var afterTask = regexp.MustCompile(`(?i)\bafter\s+(T\d+[a-zA-Z]?)\b`)

// afterPhase reads "after Phase 3" from a dependency line.
var afterPhase = regexp.MustCompile(`(?i)\bafter\s+Phase\s+(\d+)`)

// phaseOf reads the phase number a heading opens with: "Phase 3: ..." -> 3.
var phaseNum = regexp.MustCompile(`(?i)^\s*\**\s*Phase\s+(\d+)`)

// dependencyLine is one bullet of the Dependencies section, naming the
// phase it constrains: "- **Phase 2 (Setup)**: after T012."
var dependencyLine = regexp.MustCompile(`(?i)^\s*[-*]\s*\**\s*Phase\s+(\d+)`)

// dependencyEdges turns the Dependencies section's prose into ordering
// edges between tasks.
//
// The section is written for a person -- "Phase 2 (Setup): after T012",
// "Phase 4 (US2): after Phase 3" -- and every reader before the tracker had
// to take it on trust. Two shapes are read, both of which the template
// documents and real output uses: a phase that follows a NAMED TASK, and a
// phase that follows another PHASE (whose last task is the one to wait on).
//
// WHAT IS NOT INFERRED: anything the section only implies. "Independent of
// Phases 4-5 at the code level, but quickstart B3 is only meaningful after
// B1" is a sentence about judgement, and turning it into a hard link would
// order work the artifact did not order. Prose that states no dependency
// produces no edge, and the epic body still carries the whole section for a
// person to read.
func dependencyEdges(depends []string, t *Tree) []Edge {
	if t == nil || t.Epic == nil || len(depends) == 0 {
		return nil
	}
	// Every task, and the phase number its heading carries.
	type placed struct {
		id    string
		phase int
	}
	var all []placed
	_ = t.Walk(func(it, _ *Item) error {
		if it.Kind != KindTask || it.ID == "" {
			return nil
		}
		n := 0
		if m := phaseNum.FindStringSubmatch(it.Phase); m != nil {
			n, _ = strconv.Atoi(m[1])
		}
		all = append(all, placed{it.ID, n})
		return nil
	})
	if len(all) == 0 {
		return nil
	}
	inPhase := func(n int) []string {
		var out []string
		for _, p := range all {
			if p.phase == n {
				out = append(out, p.id)
			}
		}
		return out
	}

	var edges []Edge
	seen := map[string]bool{}
	add := func(blocker, blocked, why string) {
		if blocker == "" || blocked == "" || blocker == blocked {
			return
		}
		k := blocker + ">" + blocked
		if seen[k] {
			return
		}
		seen[k] = true
		edges = append(edges, Edge{Blocker: blocker, Blocked: blocked, Why: strings.TrimSpace(why)})
	}

	// ONLY THE PHASE-DEPENDENCY BULLETS. The section also carries a
	// "Parallel opportunities" list -- "Phase 2: T015, T016, T017, T019 in
	// parallel after T014" -- which says what MAY run together, not what
	// must wait. Reading it as ordering produced "T014 blocks T013", an
	// edge pointing backwards through the file and stating the opposite of
	// what the line means (FOUND ON A REAL PROJECT).
	inParallel := false
	for _, line := range depends {
		if h := strings.TrimSpace(strings.ToLower(line)); strings.HasPrefix(h, "###") {
			inParallel = strings.Contains(h, "parallel")
			continue
		}
		if inParallel {
			continue
		}
		m := dependencyLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		target, _ := strconv.Atoi(m[1])
		blocked := inPhase(target)
		if len(blocked) == 0 {
			continue
		}
		// "after T012": that task blocks every task of this phase.
		for _, am := range afterTask.FindAllStringSubmatch(line, -1) {
			for _, b := range blocked {
				add(am[1], b, line)
			}
		}
		// "after Phase 3": that phase's LAST task blocks this phase's
		// first. One edge rather than a cross product -- the phases are
		// already sequential within themselves, and N x M links on a
		// ninety-task tree is a board nobody can read.
		for _, pm := range afterPhase.FindAllStringSubmatch(line, -1) {
			n, _ := strconv.Atoi(pm[1])
			prev := inPhase(n)
			if len(prev) == 0 {
				continue
			}
			add(prev[len(prev)-1], blocked[0], line)
		}
	}
	return edges
}
