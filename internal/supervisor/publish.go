package supervisor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/orion-sdlc/orion/internal/config"
	"github.com/orion-sdlc/orion/internal/tracker"
	"github.com/orion-sdlc/orion/internal/ui"
	"github.com/orion-sdlc/orion/internal/workspace"
)

// Putting a stage's artifact where the person who asked for it can see it.
//
// The planning artifacts are committed into a repository under ~/.orion, which
// is Orion's own working area rather than anywhere the operator looks. So the
// answers given to `orion new` -- the ones the whole chain designs from --
// were invisible from the moment they were typed: present in the tracker's
// project description, then re-derived into an intent file nobody could find.
//
// This publishes the intent back onto the tracker project as a comment. A
// comment rather than the description, because the description is what
// `orion new` wrote and a stage overwriting it would destroy the record of
// what was originally asked for.
//
// BEST EFFORT, ALWAYS. The artifact is committed by the time this runs; the
// tracker copy is a convenience, and failing a stage that did its job because
// a comment did not post would be trading the work for the receipt.

// intentPublisher is the slice of the tracker this needs.
type intentPublisher interface {
	Comment(key, text string) error
}

// publishIntent copies a freshly written intent artifact onto its tracker
// project, and reports what it did for the caller to print.
//
// Returns "" when there is nothing to say -- no tracker, no project key, or a
// stage whose artifact is not the intent.
func publishIntent(t intentPublisher, ws *workspace.Workspace, cfg config.Config, stage string) string {
	if t == nil || ws == nil {
		return ""
	}
	if !strings.EqualFold(strings.TrimSpace(stage), "intent") {
		return ""
	}
	// The IDEA, not the project. A Jira project has no comments -- only
	// issues do -- so commenting with a project key is a 404 every time, and
	// it was: "commenting on CLOUDLEN: 404 Issue does not exist". The idea is
	// also the right destination on its own terms: it is where somebody
	// looking at the discovery board would go to read what this became.
	key := strings.TrimSpace(ws.Task.IdeaKey)
	if key == "" {
		return ""
	}

	body, err := os.ReadFile(filepath.Join(ws.RepoDir(), cfg.Paths.Intent, ws.Task.Slug+".md"))
	if err != nil {
		return fmt.Sprintf("could not read the intent to publish it: %v", err)
	}
	if err := t.Comment(key, intentComment(string(body))); err != nil {
		return fmt.Sprintf("could not publish the intent to %s: %v", key, err)
	}
	return "published the intent to " + key
}

// maxIntentComment bounds what is posted.
//
// Jira accepts a large comment, but a wall of text is not readable and the
// artifact is the authority in any case. A truncated comment says where the
// rest is rather than pretending to be complete.
const maxIntentComment = 30000

func intentComment(body string) string {
	body = strings.TrimSpace(body)
	if len(body) > maxIntentComment {
		body = body[:maxIntentComment] + "\n\n[truncated -- the committed artifact is the full version]"
	}
	return "Intent captured by Orion.\n\n" + body +
		"\n\n---\nThis is a copy. The artifact committed in the repository is the one " +
		"every later stage reads; edit it there, not here."
}

// trackerForPublish returns a tracker to publish with, or nil when none is
// configured.
//
// Nil rather than an error: a project with no tracker credentials is a
// supported way to run Orion, and a stage must not fail for want of a
// convenience.
func trackerForPublish() intentPublisher {
	j, err := tracker.NewJiraFromEnv()
	if err != nil {
		return nil
	}
	return j
}

// sayPublish prints a publish outcome, if there is one.
func sayPublish(msg string) {
	if msg == "" {
		return
	}
	if strings.HasPrefix(msg, "could not") {
		ui.Warn(ui.Console(), "%s", msg)
		return
	}
	fmt.Fprintf(ui.Console(), "  %s\n", msg)
}
