// Package dba decides, deterministically and for free, whether a change
// touches the database.
//
// DELIBERATELY NOT A MODEL CALL, for the reason internal/work/route.go is not
// one: this runs on every ticket, before anything has been spent, and its
// whole purpose is that a ticket which touches no data does not pay for the
// database stage. A classifier that costs a model call to decide whether to
// spend has already spent, on every ticket, forever -- and it would be
// non-deterministic about a gate whose value is that a reader can predict it.
//
// Two inputs, because the change and the ticket each know something the other
// does not. The DIFF is the ground truth for what was actually touched, and it
// is unavailable until the implementer has finished; the TICKET's labels,
// components and type are what a planner wrote down in advance, and they are
// the only signal a ticket that has not run yet has. Either one alone misses a
// real case: a "performance" ticket that ends up adding an index has no data
// label, and a ticket labelled `database` whose fix turned out to be in the
// cache layer touched no schema file.
//
// A miss in either direction is survivable and they are not symmetric. A false
// negative skips a stage that reports rather than blocks, so the change goes
// to review as it does today. A false positive spends one agent run on a
// change with no schema in it, which the report then says. So the rules below
// are written to be READABLE rather than exhaustive: a signal nobody can
// predict is worse than one that misses.
package dba

import (
	"path"
	"strings"
)

// Signal is one reason this change looks like a data change, in the words a
// person would use to check it.
//
// Carried out rather than reduced to a boolean because the stage announces why
// it is running. "This ticket touches data" is not something a reader can
// verify; "migrations/0007_add_index.sql changed" is.
type Signal struct {
	// From is where the signal came from: "diff" or "ticket".
	From string
	// What is the path, label or word that matched.
	What string
}

// pathRules are the file shapes that mean a change touched the data model.
//
// Matched on the path's SEGMENTS and its base name rather than by substring,
// the same equality-not-containment rule internal/work/route.go settled on: a
// directory called `docs/model-railway` is not an ORM model package, and a
// rule that said it was would put every one of its tickets through this stage.
var (
	// dataDirs are directory names that mean the data model at any depth.
	dataDirs = []string{
		"migrations", "migration", "migrate", "alembic",
		"models", "entities", "entity", "schema", "schemas",
	}
	// dataFiles are exact base names that are a schema by convention.
	dataFiles = []string{
		"schema.sql", "structure.sql", "schema.rb", "schema.prisma",
		"models.py", "schema.graphql",
	}
	// dataExts are extensions that are data definitions whatever they sit in.
	dataExts = []string{".sql", ".prisma"}
)

// TicketWords are the markers a planner writes that mean this ticket is about
// the database.
//
// EXPORTED because internal/work's routing table publishes exactly this list
// as the database architect's keywords. One list, in one place: a planner that
// learned the routing marker has learned the stage trigger too, and a second
// copy would be a stage triggering on words `orion routes` does not print --
// which is the drift OR-191 established there is exactly one vocabulary to
// avoid.
//
// Matched against the ticket's issue type, components and labels by equality,
// never against its free-text summary: a summary is prose, and "we should
// index the docs page" is not a database ticket.
var TicketWords = []string{
	"database", "db", "dba", "schema", "migration", "migrations", "sql",
	"index", "indexing", "query", "data-model",
}

// TouchesPath reports whether one changed path is part of the data model.
func TouchesPath(p string) bool {
	p = strings.TrimSpace(strings.ToLower(strings.ReplaceAll(p, `\`, "/")))
	if p == "" {
		return false
	}
	base := path.Base(p)
	for _, f := range dataFiles {
		if base == f {
			return true
		}
	}
	for _, e := range dataExts {
		if strings.HasSuffix(base, e) {
			return true
		}
	}
	// Every segment except the last, which is the file. A file called
	// `migrations.go` is code ABOUT migrations, and reading it as a migration
	// would put the runner's own package through this stage on every change.
	segs := strings.Split(p, "/")
	for _, s := range segs[:len(segs)-1] {
		for _, d := range dataDirs {
			if s == d {
				return true
			}
		}
	}
	return false
}

// TouchesTicket reports whether a ticket's own markers say it is about data.
// fields are the issue type, components and labels, exactly as
// internal/work/route.go assembles them.
func TouchesTicket(fields []string) (string, bool) {
	for _, f := range fields {
		f = strings.TrimSpace(strings.ToLower(f))
		for _, w := range TicketWords {
			if f == w {
				return f, true
			}
		}
	}
	return "", false
}

// Signals is every reason this change looks like a data change, from the paths
// the diff touched and the markers on the ticket.
//
// Empty means the stage does not run. Every path is reported rather than only
// the first, capped, because the stage's opening line is the operator's only
// chance to say "that is not a schema change" before the money is spent.
func Signals(paths, ticketFields []string) []Signal {
	var out []Signal
	for _, p := range paths {
		if TouchesPath(p) {
			out = append(out, Signal{From: "diff", What: strings.TrimSpace(p)})
		}
		if len(out) == MaxSignals {
			break
		}
	}
	if w, ok := TouchesTicket(ticketFields); ok && len(out) < MaxSignals {
		out = append(out, Signal{From: "ticket", What: w})
	}
	return out
}

// MaxSignals caps what the announcement lists. A change that rewrote forty
// migrations has made its point by the fifth: the line exists to let a reader
// recognise the change, not to enumerate it.
const MaxSignals = 5

// Reason renders the signals as the one line the stage opens with.
func Reason(sigs []Signal) string {
	if len(sigs) == 0 {
		return "nothing in this change touches the data model"
	}
	parts := make([]string, 0, len(sigs))
	for _, s := range sigs {
		parts = append(parts, s.What+" ("+s.From+")")
	}
	return strings.Join(parts, ", ")
}
