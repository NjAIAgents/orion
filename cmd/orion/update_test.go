package main

import "testing"

// The one hard requirement of OR-92: the update notice never appears in hook
// mode. Hooks fire on every matching tool call inside a Claude Code session,
// so a version notice there would be printed hundreds of times a day into
// somebody's editor -- and would be the single most annoying thing Orion
// does. The rest of the list is the commands a person runs and READS.
func TestOnlyReadableCommandsShowTheUpdateNotice(t *testing.T) {
	for _, cmd := range []string{"status", "doctor", "watch", "collect", "work", "init"} {
		if !showsUpdateNotice(cmd) {
			t.Errorf("%q must show the notice; a version gap is otherwise silent", cmd)
		}
	}
	for _, cmd := range []string{
		"hook",         // the hard one
		"update-check", // the refresh child: it must never print, or it would recurse into itself
		"version", "explore", "fan", "logs", "queue", "report", "ls", "open",
	} {
		if showsUpdateNotice(cmd) {
			t.Errorf("%q must not show the notice", cmd)
		}
	}
}
