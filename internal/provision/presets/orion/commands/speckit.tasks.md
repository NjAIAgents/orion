## Orion runs this headless — these rules add to the instructions below

Orion parses the task list you write into tracker items: one epic, one story
per user-story phase, one item per task. An agent then picks up a single item
and works it with nothing but that item's text in front of it — not this file,
not the plan, not the spec. So two things that are optional in a task list are
required here, and they are the two an agent cannot infer:

1. **Every task line is followed by an indented `Done when:` line.** One line,
   naming something a reviewer could check without reading the plan: a file
   exists and contains X, a named command exits 0, a named test passes, a
   figure appears on a page. Not "the code is written" — that restates the
   task. Prefer the check the task itself implies:

       - [ ] T012 [P] [US1] Create the User model in src/models/user.py
         Done when: src/models/user.py defines User with the fields data-model.md
         lists, and `go test ./src/models/` passes.

2. **Every user-story phase carries an `Acceptance criteria:` block**, after
   its `**Goal**` and `**Independent Test**` lines, in Given/When/Then form.
   COPY OR NARROW the Acceptance Scenarios that story already has in spec.md
   rather than writing new ones — the spec is the agreed text, and two
   wordings of one requirement is one of them going stale:

       **Acceptance criteria** (from spec.md US1 scenarios 1-3):
       1. **Given** no assessment exists, **When** a build change is proposed,
          **Then** the change is refused until the assessment concludes viable.

3. **Mark a task no agent can do with `[HUMAN]`**, after the id: work behind a
   credential a person holds, a conversation with another team, a click in a
   console, a protected path a review must carry. Orion files these as tickets
   like any other and does not offer them to an agent, so the marker is what
   stops a run being spent on a refusal:

       - [ ] T075 [HUMAN] Register the OIDC client in the identity provider
         Done when: the client id and redirect URI are recorded in §4 of the runbook.

Everything else about the task list — the checklist format, the ids, the `[P]`
markers, the `[USn]` tags, the file paths, the phases, the Dependencies
section — is unchanged and still required exactly as described below.

{CORE_TEMPLATE}
