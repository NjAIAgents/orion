# 0010: The routing vocabulary is a published contract, and five actors are routable

- Status: Accepted — shipped
- Date: 2026-08-29
- Ticket: OR-191 (follows OR-171, which added routing at all)

## Context

`internal/work/route.go` picks the actor that works a ticket by looking at
the ticket's issue type, components and labels. Nothing that CREATES a
ticket knew those keywords existed, so the metadata routing depends on was
set by luck — and in practice never. Every OR ticket carried the `ORION`
label and a type of Task, Bug or Story, none of which matched any rule, so
every one of them took the default and the run log said so on every run:

```
backend developer: no issue type, component or label matched a route
```

The default is deliberate and announcing it is right (OR-171: a silent
fallthrough is how the frontend actor stayed unreachable for months). The
problem was that the announcement was correct every time, because the
information routing wanted was never written down at planning time.

The table's size made publishing it pointless on its own. It had two rules —
`docs` and `frontend` — against a roster of eleven actors, so even a planner
that tagged perfectly could express two destinations besides the default.
"Plan accordingly" would have meant "occasionally write the word docs".
Expanding the table and publishing it are therefore one decision.

## Decision

**Five actors are routable**: the docs engineer, the frontend developer, the
architect and the product manager, plus the backend developer as the default
a ticket with no marker takes.

Architect and product manager are added because they were the only roster
entries a ticket could not reach at all. Both exist today solely as
*advisors* — `internal/advise` hands them a free-text question raised inside
someone else's run and takes back an answer. Working a ticket end to end is
a different job, so a route to them is a first way in, not a second one.

**Every other actor stays unroutable, and why is recorded** in `otherPaths`
in `route.go` and printed by `orion routes`:

| Actor | Reached by |
|---|---|
| dispatcher (`router`) | routes the free-text question an implementer stops on, inside a run |
| QA engineer | runs after every implementation, on whatever actor worked the ticket |
| devops engineer | repairs a red build, from the CI verdict |
| log triage | reads the failing CI log that the repair run then carries |
| PR writer (`describer`) | writes the pull request body, on every ticket |
| code explorer | answers one question about the repository, inside a run |

Each of these already has an invocation path. A label that reached one of
them would be a SECOND way to invoke one thing, which is how OR-176
happened: the two ways drift, and the failure surfaces as a stage that ran
twice or not at all.

**The table is published, not restated.** `orion routes` prints it actor by
actor with the exact keywords each accepts. The decompose stage prompt and
the post-provision hint tell the planner to run that command; neither
carries a copy of the vocabulary, because a second copy drifts from the one
routing actually reads. Per the precedence rule (0001), Orion owns the
tracker contract and the nj-agents `/pm-plan` skill supplies the
decomposition methodology inside the stage: Orion states the vocabulary, the
planner consumes it, and ownership of the table is never delegated.

**`orion queue` reports the routing distribution** of the work it is about to
show, before anything runs. A queue that is entirely default is either
correct or a planning failure, and until the split is on screen the two are
indistinguishable.

## Consequences

- Matching stays EQUALITY, case-insensitive, over an ordered slice.
  Substring matching was rejected in OR-171 for a stated reason — a
  component named `docsite-infra` is not a documentation ticket — and
  expanding the table does not reopen it. Precedence remains the written
  order.
- Routing stays a deterministic lookup, never a model call. The temptation
  the larger table creates is to infer the right actor from the summary when
  no marker is present; that is the same mistake with a friendlier face. An
  unmarked ticket defaults, and the fix belongs at planning time.
- Adding an actor to the roster now forces a decision: a test requires every
  configurable actor to be either routable, the default, or listed in
  `otherPaths` with a reason. "Not in the table" and "nobody got round to
  it" no longer look identical from outside.
- Keyword sets do not overlap, so precedence only decides a ticket carrying
  two markers. `design` is deliberately absent from the architect's set: it
  reads as UI design at least as often as system design.
- The default's announcement keeps its tone fixed. On the happy path a
  ticket with no marker is a backend ticket, so it now reads "defaulting to
  the implementer; no routing marker on this ticket" rather than phrasing a
  normal outcome as a miss.
