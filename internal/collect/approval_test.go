package collect

import (
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/slack"
)

type fakeSlack struct {
	reactions []slack.Reaction
	replies   []slack.Message
	names     map[string]string
	rErr      error
	pErr      error
}

func (f *fakeSlack) Reactions(string, string) ([]slack.Reaction, error) {
	return f.reactions, f.rErr
}
func (f *fakeSlack) Replies(string, string) ([]slack.Message, error) {
	return f.replies, f.pErr
}
func (f *fakeSlack) UserName(id string) string {
	if n, ok := f.names[id]; ok {
		return n
	}
	return id
}

const bot = "UBOT"

var allow = []string{"navjyot"}

func read(t *testing.T, f *fakeSlack) Decision {
	t.Helper()
	if f.names == nil {
		f.names = map[string]string{"UNAV": "navjyot", "UOTHER": "someone-else", bot: "orion"}
	}
	d, err := ReadDecision(f, "C1", "111.222", bot, allow)
	if err != nil {
		t.Fatalf("ReadDecision: %v", err)
	}
	return d
}

func TestATickFromAnAllowlistedPersonApproves(t *testing.T) {
	d := read(t, &fakeSlack{reactions: []slack.Reaction{
		{Name: "white_check_mark", Users: []string{"UNAV"}},
	}})
	if !d.Approved || d.By != "navjyot" {
		t.Fatalf("expected approval by navjyot, got %+v", d)
	}
}

// Orion adds the emoji to its own message so a phone user can tap rather than
// hunt for them. Counting the bot's own reaction would make every request
// self-approving -- the obvious way to build the affordance is also the
// obvious way to lose the entire gate.
func TestTheBotsOwnReactionNeverApproves(t *testing.T) {
	d := read(t, &fakeSlack{reactions: []slack.Reaction{
		{Name: "white_check_mark", Users: []string{bot}},
	}})
	if d.Approved {
		t.Fatal("Orion approved its own merge request")
	}
}

// Being in the channel is not authority. A room contains people who have no
// idea what they are approving.
func TestSomeoneNotOnTheAllowlistCannotApprove(t *testing.T) {
	d := read(t, &fakeSlack{reactions: []slack.Reaction{
		{Name: "white_check_mark", Users: []string{"UOTHER"}},
	}})
	if d.Approved {
		t.Fatal("a non-allowlisted member approved a merge")
	}
	if !strings.Contains(d.Why, "allowlist") {
		t.Errorf("the reason must name the allowlist so it can be fixed, got %q", d.Why)
	}
}

// An empty allowlist means nobody, not everybody. Defaulting to open would
// turn a merge gate into decoration on the first repository somebody forgot
// to configure.
func TestAnEmptyAllowlistApprovesNobody(t *testing.T) {
	f := &fakeSlack{reactions: []slack.Reaction{{Name: "white_check_mark", Users: []string{"UNAV"}}}}
	f.names = map[string]string{"UNAV": "navjyot"}
	d, err := ReadDecision(f, "C1", "1", bot, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.Approved {
		t.Fatal("an unconfigured allowlist let a merge through")
	}
}

// A rejection raised after an approval is an objection to something the
// approver missed. First-answer-wins would merge straight over it.
func TestARejectionBeatsAnApprovalHoweverLateItArrives(t *testing.T) {
	d := read(t, &fakeSlack{
		reactions: []slack.Reaction{
			{Name: "white_check_mark", Users: []string{"UNAV"}},
			{Name: "x", Users: []string{"UNAV"}},
		},
	})
	if d.Approved {
		t.Fatal("approved despite a rejection on the same message")
	}
	if !d.Rejected {
		t.Fatal("the rejection was not recorded")
	}
}

func TestATypedApprovalCounts(t *testing.T) {
	d := read(t, &fakeSlack{replies: []slack.Message{
		{User: "UNAV", Text: "LGTM, ship it"},
	}})
	if !d.Approved {
		t.Fatalf("a typed approval must count; phone users react, laptop users type: %+v", d)
	}
}

// Substring matching would find "approve" inside "would not approve" and
// merge code over a plain objection.
func TestApprovalWordsMatchAsWordsNotSubstrings(t *testing.T) {
	for _, text := range []string{
		"I would not approve this yet",
		"do not merge",
		"hold off",
	} {
		d := read(t, &fakeSlack{replies: []slack.Message{{User: "UNAV", Text: text}}})
		if d.Approved {
			t.Errorf("%q was read as an approval", text)
		}
	}
}

func TestSkinTonedReactionsStillCount(t *testing.T) {
	d := read(t, &fakeSlack{reactions: []slack.Reaction{
		{Name: "+1::skin-tone-3", Users: []string{"UNAV"}},
	}})
	if !d.Approved {
		t.Fatal("a skin-toned thumbs up is still a thumbs up")
	}
}

// Reactions and replies are independent surfaces. Losing one -- a scope
// granted for one and not the other, most likely -- must not silently
// disable approvals on the other.
func TestOneUnreadableSurfaceStillAllowsTheOther(t *testing.T) {
	d := read(t, &fakeSlack{
		rErr:    errNoScope,
		replies: []slack.Message{{User: "UNAV", Text: "approved"}},
	})
	if !d.Approved {
		t.Fatal("a readable reply surface should still carry an approval")
	}
}

func TestBothSurfacesUnreadableIsAnError(t *testing.T) {
	f := &fakeSlack{rErr: errNoScope, pErr: errNoScope}
	f.names = map[string]string{}
	if _, err := ReadDecision(f, "C1", "1", bot, allow); err == nil {
		t.Fatal("expected an error when nothing can be read; silence here would look like 'not approved yet' forever")
	}
}

var errNoScope = errScope("missing_scope")

type errScope string

func (e errScope) Error() string { return string(e) }
