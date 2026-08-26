package main

import "testing"

// The bug these cover, stated once: Orion posted every notification for a
// week into a private channel whose only member was the bot. Slack accepted
// every message. chat.postMessage returned ok. The messages are really there.
// No human could read one, find one by search, or learn the channel existed.
//
// Every check Orion had said PASS. The token was valid, the channel resolved,
// the post succeeded, the approval scopes were granted -- and the feature was
// completely broken, because "delivered" and "received" are different claims
// and only the first one was being tested.
//
// So these tests are about the difference between those two claims.

func TestABotAloneInAChannelIsNotAnAudience(t *testing.T) {
	const bot = "U0BSA3A4JRM"

	people := humansAmong([]string{bot}, bot)
	if len(people) != 0 {
		t.Fatalf("the bot counted itself as an audience: %v", people)
	}
}

func TestHumansAmong(t *testing.T) {
	const bot = "UBOT"

	cases := []struct {
		name    string
		members []string
		want    int
	}{
		{"empty channel", nil, 0},
		{"bot only -- the failure that shipped", []string{bot}, 0},
		{"one person", []string{bot, "UNAV"}, 1},
		{"people, no bot (a channel it posts to via chat:write.public)",
			[]string{"UNAV", "UOTHER"}, 2},
		// A blank entry is not a person. Counting one would report an
		// audience that does not exist, which is the exact failure being
		// fixed, reintroduced by a whitespace bug.
		{"blank ids are not people", []string{bot, "", "  "}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := len(humansAmong(c.members, bot)); got != c.want {
				t.Errorf("humansAmong(%v) = %d people, want %d", c.members, got, c.want)
			}
		})
	}
}

// A second bot is still not a person, but Orion cannot tell -- it only knows
// its OWN id. Documented as a known limit rather than pretended away: the
// check catches the case that actually happens (a freshly created private
// channel) and does not claim to catch a room full of other apps.
func TestOnlyOrionsOwnBotIsRecognised(t *testing.T) {
	people := humansAmong([]string{"UORION", "USOMEOTHERBOT"}, "UORION")
	if len(people) != 1 {
		t.Fatalf("want the other id treated as a person, got %v", people)
	}
}

// countHumans backs the init-time gate that fcia needed and did not have.
//
// The old code returned early unless the channel had just been CREATED, on
// the reasoning that a found channel "already has whoever belongs in it".
// The channel that stranded fcia was FOUND -- created in an earlier, broken
// init and then reused -- so the one case that mattered was the one case
// skipped. There is no test for that early return because the fix was to
// delete it; this covers the judgement that replaced it.
func TestCountingWhoCanRead(t *testing.T) {
	// Standing in for AuthTest returning the bot's own id.
	const self = "UORION"

	cases := []struct {
		name    string
		members []string
		want    int
	}{
		{"the fcia failure: bot alone", []string{self}, 0},
		{"empty channel", nil, 0},
		{"one person", []string{self, "UNAV"}, 1},
		{"several", []string{self, "UNAV", "UOTHER"}, 2},
		{"blanks are not people", []string{self, "", " "}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := len(humansAmong(c.members, self)); got != c.want {
				t.Errorf("counted %d readers in %v, want %d", got, c.members, c.want)
			}
		})
	}
}
