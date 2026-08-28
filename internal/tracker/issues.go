package tracker

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// Issue reading and mutation, for the label-driven queue.
//
// The design this serves: a Jira label is the trigger AND the state. Orion
// polls for issues carrying the queue label, works them in backlog order,
// and moves the label as it goes. Keeping the state on the ticket rather
// than in a local file means a crash resumes correctly, and you can see what
// Orion believes it is doing by looking at the board rather than by reading
// a file on someone's laptop.

// Issue is the part of a Jira issue Orion acts on.
type Issue struct {
	Key         string
	Summary     string
	Description string
	Status      string
	// StatusCategory is Jira's coarse grouping of Status: "new",
	// "indeterminate" or "done". Read as well as the name because the name is
	// per-project -- Done, Closed, Cancelled, Won't Do are all different
	// words for the same category -- and Orion needs "is this finished?"
	// answered for a workflow it has never seen.
	StatusCategory string
	Labels         []string
	Priority       string
	// Rank is Jira's backlog ordering field. It is what makes "work these
	// in this order" expressible by dragging tickets, with no syntax.
	Rank string
	URL  string
	// Parent is the key of this issue's parent, or empty. Set for Jira
	// sub-tasks and for the child issues of a team-managed project, which
	// both use the same field -- so one query and one field cover both
	// hierarchies without Orion knowing which shape a project uses.
	Parent string
}

// Search runs JQL and returns issues in the order Jira gave them, which is
// the order the caller asked for.
//
// maxResults is capped rather than paginated on purpose: a queue that has
// grown past a hundred ready tickets is a runaway, not a workload, and
// silently working through it is the failure this whole design guards
// against.
func (j *Jira) Search(jql string, maxResults int) ([]Issue, error) {
	if maxResults <= 0 || maxResults > 100 {
		maxResults = 100
	}
	q := url.Values{}
	q.Set("jql", jql)
	q.Set("maxResults", fmt.Sprint(maxResults))
	q.Set("fields", "summary,description,status,labels,priority,parent")

	code, body, err := j.do("GET", "/rest/api/3/search/jql?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if code == 400 {
		// A bad JQL is a configuration mistake, and Jira explains it well.
		// Passing that explanation through beats "search failed".
		return nil, fmt.Errorf("Jira rejected the query: %s\n  JQL: %s", snippet(body), jql)
	}
	if code >= 400 {
		return nil, fmt.Errorf("searching issues: %d %s", code, snippet(body))
	}

	var res struct {
		Issues []struct {
			Key    string `json:"key"`
			Fields struct {
				Summary     string          `json:"summary"`
				Description json.RawMessage `json:"description"`
				Labels      []string        `json:"labels"`
				Status      struct {
					Name     string `json:"name"`
					Category struct {
						Key string `json:"key"`
					} `json:"statusCategory"`
				} `json:"status"`
				Priority struct {
					Name string `json:"name"`
				} `json:"priority"`
				Parent struct {
					Key string `json:"key"`
				} `json:"parent"`
			} `json:"fields"`
		} `json:"issues"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("parsing search results: %w", err)
	}

	out := make([]Issue, 0, len(res.Issues))
	for _, i := range res.Issues {
		out = append(out, Issue{
			Key:            i.Key,
			Summary:        i.Fields.Summary,
			Description:    flattenADF(i.Fields.Description),
			Status:         i.Fields.Status.Name,
			StatusCategory: i.Fields.Status.Category.Key,
			Labels:         i.Fields.Labels,
			Priority:       i.Fields.Priority.Name,
			Parent:         i.Fields.Parent.Key,
			URL:            j.BaseURL + "/browse/" + i.Key,
		})
	}
	return out, nil
}

// GetIssue fetches one issue.
func (j *Jira) GetIssue(key string) (*Issue, error) {
	code, body, err := j.do("GET", "/rest/api/3/issue/"+url.PathEscape(key)+
		"?fields=summary,description,status,labels", nil)
	if err != nil {
		return nil, err
	}
	if code == 404 {
		return nil, fmt.Errorf("issue %s not found on %s", key, j.BaseURL)
	}
	if code >= 400 {
		return nil, fmt.Errorf("fetching %s: %d %s", key, code, snippet(body))
	}
	var i struct {
		Key    string `json:"key"`
		Fields struct {
			Summary     string          `json:"summary"`
			Description json.RawMessage `json:"description"`
			Labels      []string        `json:"labels"`
			Status      struct {
				Name     string `json:"name"`
				Category struct {
					Key string `json:"key"`
				} `json:"statusCategory"`
			} `json:"status"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(body, &i); err != nil {
		return nil, err
	}
	return &Issue{
		Key:            i.Key,
		Summary:        i.Fields.Summary,
		Description:    flattenADF(i.Fields.Description),
		Status:         i.Fields.Status.Name,
		StatusCategory: i.Fields.Status.Category.Key,
		Labels:         i.Fields.Labels,
		URL:            j.BaseURL + "/browse/" + i.Key,
	}, nil
}

// SetLabels adds and removes labels in one request.
//
// Jira's update API takes add/remove operations rather than a whole list,
// which matters: writing the full set would clobber a label someone added
// while Orion was working, and the queue label is how a human cancels a job.
func (j *Jira) SetLabels(key string, add, remove []string) error {
	var ops []map[string]any
	for _, l := range add {
		if l = strings.TrimSpace(l); l != "" {
			ops = append(ops, map[string]any{"add": l})
		}
	}
	for _, l := range remove {
		if l = strings.TrimSpace(l); l != "" {
			ops = append(ops, map[string]any{"remove": l})
		}
	}
	if len(ops) == 0 {
		return nil
	}
	payload := map[string]any{"update": map[string]any{"labels": ops}}
	code, body, err := j.do("PUT", "/rest/api/3/issue/"+url.PathEscape(key), payload)
	if err != nil {
		return err
	}
	if code >= 400 {
		return fmt.Errorf("updating labels on %s: %d %s", key, code, snippet(body))
	}
	return nil
}

// Comment posts a plain-text comment, wrapped in the document format Jira
// Cloud's v3 API requires.
func (j *Jira) Comment(key, text string) error {
	payload := map[string]any{"body": adfParagraphs(text)}
	code, body, err := j.do("POST",
		"/rest/api/3/issue/"+url.PathEscape(key)+"/comment", payload)
	if err != nil {
		return err
	}
	if code >= 400 {
		return fmt.Errorf("commenting on %s: %d %s", key, code, snippet(body))
	}
	return nil
}

// Transitions lists the moves available from the issue's current status.
type Transition struct {
	ID   string
	Name string
	To   string
}

func (j *Jira) Transitions(key string) ([]Transition, error) {
	code, body, err := j.do("GET", "/rest/api/3/issue/"+url.PathEscape(key)+"/transitions", nil)
	if err != nil {
		return nil, err
	}
	if code >= 400 {
		return nil, fmt.Errorf("listing transitions for %s: %d %s", key, code, snippet(body))
	}
	var res struct {
		Transitions []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			To   struct {
				Name string `json:"name"`
			} `json:"to"`
		} `json:"transitions"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, err
	}
	out := make([]Transition, 0, len(res.Transitions))
	for _, t := range res.Transitions {
		out = append(out, Transition{ID: t.ID, Name: t.Name, To: t.To.Name})
	}
	return out, nil
}

// TransitionTo moves an issue to a named status.
//
// Matches on the DESTINATION status as well as the transition name, and
// case-insensitively, because workflows name the same move differently
// ("Start Progress" vs "In Progress") and a hardcoded name works on one
// Jira project and silently no-ops on the next. Returns the available
// options when nothing matches, so the failure is actionable.
func (j *Jira) TransitionTo(key, status string) error {
	ts, err := j.Transitions(key)
	if err != nil {
		return err
	}
	want := strings.ToLower(strings.TrimSpace(status))
	for _, t := range ts {
		if strings.ToLower(t.To) == want || strings.ToLower(t.Name) == want {
			code, body, err := j.do("POST", "/rest/api/3/issue/"+url.PathEscape(key)+"/transitions",
				map[string]any{"transition": map[string]any{"id": t.ID}})
			if err != nil {
				return err
			}
			if code >= 400 {
				return fmt.Errorf("transitioning %s to %s: %d %s", key, status, code, snippet(body))
			}
			return nil
		}
	}
	var names []string
	for _, t := range ts {
		names = append(names, fmt.Sprintf("%s (-> %s)", t.Name, t.To))
	}
	return fmt.Errorf("no transition to %q from %s's current status.\n  Available: %s",
		status, key, strings.Join(names, ", "))
}

// adfParagraphs wraps plain text in Atlassian Document Format.
func adfParagraphs(text string) map[string]any {
	var content []any
	for _, line := range strings.Split(text, "\n") {
		para := map[string]any{"type": "paragraph"}
		if line != "" {
			para["content"] = []any{map[string]any{"type": "text", "text": line}}
		}
		content = append(content, para)
	}
	return map[string]any{"type": "doc", "version": 1, "content": content}
}

// flattenADF turns an Atlassian Document Format body back into plain text.
//
// Jira Cloud returns descriptions as a nested document, not a string. An
// agent handed the raw JSON would be reading markup instead of the
// requirement, so this walks the tree and keeps the text nodes.
func flattenADF(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	// Older instances, and some API paths, still return a plain string.
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var node adfNode
	if json.Unmarshal(raw, &node) != nil {
		return ""
	}
	var b strings.Builder
	walkADF(&node, &b)
	return strings.TrimSpace(b.String())
}

type adfNode struct {
	Type    string         `json:"type"`
	Text    string         `json:"text"`
	Content []adfNode      `json:"content"`
	Attrs   map[string]any `json:"attrs"`
}

func walkADF(n *adfNode, b *strings.Builder) {
	// Keep the bullet. Acceptance criteria arrive as a bulleted list, and
	// flattening them to bare lines separated by blanks loses the fact that
	// they are a list of separate conditions -- which is exactly the part of
	// a ticket an agent most needs to read as discrete requirements.
	if n.Type == "listItem" {
		b.WriteString("- ")
	}
	if n.Text != "" {
		b.WriteString(n.Text)
	}
	for i := range n.Content {
		walkADF(&n.Content[i], b)
	}
	switch n.Type {
	case "paragraph", "heading", "codeBlock", "blockquote":
		b.WriteString("\n")
	}
}

// The queue's state machine lives in labels on the ticket.
//
//	<QueueLabel>   queued: a human asked for this
//	orion-working  claimed: an agent is running, holding the repo's job slot
//	orion-ci-wait  pushed: a pull request is open and CI is running
//	orion-failed   stopped: needs a person
//
// Done is the ABSENCE of all four, deliberately. A "done" label would
// accumulate forever on every ticket Orion ever touched, and the tracker
// already records completion in the issue's own status.
//
// ci-wait is separate from working for two reasons. It releases the repo's
// job slot, so a twenty-minute CI run does not stall the rest of the queue
// behind a machine nobody is waiting on. More importantly it makes the wait
// CRASH-SAFE: if the state lived only in a blocked process, a daemon that
// died mid-wait would forget the pull request existed, and the obvious retry
// would re-run the agent and open a SECOND pull request for the same ticket.
// On the ticket, a restarted watcher sees ci-wait, finds the existing pull
// request and resumes polling without spending anything.
// QueueLabelDefault is the label a ticket carries to ask for work. It is
// per-project config (tracker.queue_label), but a watcher spans projects and
// a cross-project query can only ask about one label, so this is the value
// they are expected to share.
const QueueLabelDefault = "ORION"

const (
	LabelWorking = "orion-working"
	LabelCIWait  = "orion-ci-wait"
	LabelFailed  = "orion-failed"
)

// StatusCategoryDone is Jira's terminal category. Every workflow has one,
// whatever it calls the statuses inside it.
const StatusCategoryDone = "Done"

// Resolved reports whether the issue is finished, whatever its workflow calls
// that -- Done, Closed, Cancelled, Won't Do.
//
// Unknown means NOT resolved. A tracker that did not return the category, or
// a fake in a test that does not set it, must not silently make every ticket
// look finished: that would stop the queue entirely, a far worse failure than
// the one this guards.
func (i Issue) Resolved() bool {
	return strings.EqualFold(strings.TrimSpace(i.StatusCategory), StatusCategoryDone)
}

// Managed are every label Orion owns, for the queue query and for clearing
// state when a ticket is finished or requeued.
func Managed(queueLabel string) []string {
	return []string{queueLabel, LabelWorking, LabelCIWait, LabelFailed}
}

// StaleLocks returns the keys of issues that are FINISHED but still carry
// the claim label.
//
// The label is the queue's lock, and nothing outside Orion's own close path
// clears it -- so a ticket somebody fixed and moved to Done by hand keeps it
// and every watch tick reports that ticket as still running (OR-125). The
// watcher now clears one when it trips over it; this is so `orion queue`
// names the condition instead of showing a "working" ticket whose status
// says Done and leaving the reader to reconcile the two.
func StaleLocks(issues []Issue) []string {
	var out []string
	for _, i := range issues {
		if !i.Resolved() {
			continue
		}
		for _, l := range i.Labels {
			if strings.EqualFold(l, LabelWorking) {
				out = append(out, i.Key)
				break
			}
		}
	}
	return out
}

// State reports where an issue sits in Orion's queue, given the label that
// marks work as requested.
func State(labels []string, queueLabel string) string {
	has := func(want string) bool {
		for _, l := range labels {
			if strings.EqualFold(l, want) {
				return true
			}
		}
		return false
	}
	// Order matters: a ticket can briefly carry two labels if a write was
	// interrupted between the remove and the add. Reporting the more urgent
	// one is the safe reading -- failed outranks everything, and an agent
	// still running outranks a pull request that may be stale.
	switch {
	case has(LabelFailed):
		return "failed"
	case has(LabelWorking):
		return "working"
	case has(LabelCIWait):
		return "ci-wait"
	case has(queueLabel):
		return "queued"
	}
	return ""
}
