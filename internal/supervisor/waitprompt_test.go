package supervisor

import (
	"strings"
	"testing"
)

// OR-207: the prompt must tell the agent HOW to wait for a long command.
//
// OR-189 and OR-191 both backgrounded ./scripts/test.sh -- nine minutes, at
// the time -- polled its output file while it ran, and tripped the
// identical-repeat breaker with their work finished, green and uncommitted.
// Both reached for polling because nothing had ever told them another way
// existed. Enforcing the rule in the breaker without stating it in the prompt
// fixes half the problem and leaves the expensive half.
func TestTheTicketPromptSaysHowToWaitForALongCommand(t *testing.T) {
	p := TicketPrompt("OR-1", "do the thing", "details", "https://x/1", "", nil)

	if !strings.Contains(p, "WAITING FOR A LONG COMMAND") {
		t.Fatalf("the prompt must say how to wait:\n%s", p)
	}
	for _, want := range []string{"FOREGROUND", "Do NOT launch it in the background"} {
		if !strings.Contains(p, want) {
			t.Errorf("the prompt must contain %q:\n%s", want, p)
		}
	}
	// Said whether or not this repository happens to have scripts/test.sh --
	// every repository has something slow enough to be waited for, and an
	// instruction that appears only sometimes is one an agent cannot rely on.
	if strings.Contains(p, "./scripts/test.sh") {
		t.Errorf("the waiting instruction must not depend on one repository's script:\n%s", p)
	}
}
