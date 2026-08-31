package collect

import (
	"fmt"
	"strings"
	"testing"
)

// resolver answers the outward lookup and counts how often it was asked.
type resolver struct {
	ids   map[string]string
	calls int
}

func (r *resolver) MemberID(who string) (string, error) {
	r.calls++
	if id, ok := r.ids[who]; ok {
		return id, nil
	}
	return "", fmt.Errorf("no Slack user is named %q", who)
}

// The bug this ticket exists for: the approval request named the approver in
// plain text, Slack notified nobody, and the merge waited on a person who was
// never told. Only the <@U...> form raises a notification.
func TestEveryResolvableApproverIsMentionedByMemberID(t *testing.T) {
	r := &resolver{ids: map[string]string{
		"navjyot":         "U012ABCDEF",
		"sam@example.com": "U345GHIJKL",
	}}

	tags, unresolved := approverTags(r, []string{"navjyot", "sam@example.com"})

	if len(unresolved) != 0 {
		t.Fatalf("nothing should have failed: %v", unresolved)
	}
	want := []string{"<@U012ABCDEF>", "<@U345GHIJKL>"}
	for i, w := range want {
		if tags[i] != w {
			t.Errorf("tag %d = %q, want %q -- a bare name notifies nobody", i, tags[i], w)
		}
	}
}

// Degrade, never fail. An approval request that does not send because a
// lookup failed is far worse than one that does not tag -- but the run has to
// say so, or a typo in merge_approvers is a silent non-notification.
func TestAnUnresolvableApproverFallsBackToPlainTextAndIsReported(t *testing.T) {
	r := &resolver{ids: map[string]string{"navjyot": "U012ABCDEF"}}

	tags, unresolved := approverTags(r, []string{"navjyot", "typo"})

	if len(tags) != 2 || tags[1] != "typo" {
		t.Fatalf("the unresolvable name must still be named: %v", tags)
	}
	if tags[0] != "<@U012ABCDEF>" {
		t.Errorf("one failure must not cost the others their mention: %v", tags)
	}
	if len(unresolved) != 1 || !strings.Contains(unresolved[0], "typo") {
		t.Fatalf("the failure must be reported, naming who: %v", unresolved)
	}
	if !strings.Contains(unresolved[0], "notified") {
		t.Errorf("the report must say what was lost, not merely that a lookup failed: %q",
			unresolved[0])
	}
}

// The fallback prints a configured value verbatim, so a value SHAPED like a
// broadcast would reach every member of a channel Orion made per project --
// people with no standing to approve. The mention path refuses those on
// purpose; the fallback must not smuggle one back in.
func TestNoPathEverRendersAChannelWideMention(t *testing.T) {
	r := &resolver{}

	tags, _ := approverTags(r, []string{"<!channel>", "<!here>", "@everyone"})

	for _, tag := range tags {
		if strings.Contains(tag, "<!") || strings.Contains(tag, "<@") {
			t.Errorf("%q would broadcast; a fallback must be inert text", tag)
		}
	}
}

// An empty or whitespace-only entry in slack.merge_approvers names nobody --
// it must be dropped silently rather than rendered as a blank tag or asked
// of the resolver at all.
func TestEmptyAndWhitespaceApproversAreSkipped(t *testing.T) {
	r := &resolver{ids: map[string]string{"navjyot": "U012ABCDEF"}}

	tags, unresolved := approverTags(r, []string{"", "   ", "navjyot", "\t"})

	if len(tags) != 1 || tags[0] != "<@U012ABCDEF>" {
		t.Fatalf("blank entries must not produce tags: %v", tags)
	}
	if len(unresolved) != 0 {
		t.Errorf("a blank entry is not a failed lookup: %v", unresolved)
	}
	if r.calls != 1 {
		t.Errorf("the resolver was asked about a blank entry: %d calls", r.calls)
	}
}

// Slack is not configured for approvals at all. The message still has to say
// who may approve, and nothing may panic on the way there.
func TestNoResolverStillNamesTheApprovers(t *testing.T) {
	tags, unresolved := approverTags(nil, []string{"navjyot"})

	if len(tags) != 1 || tags[0] != "navjyot" {
		t.Fatalf("the approver must still be named: %v", tags)
	}
	if len(unresolved) != 1 {
		t.Errorf("a request that tagged nobody must say so: %v", unresolved)
	}
}
