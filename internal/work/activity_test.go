package work

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/supervisor"
)

// ActivityLogger is the one logger every supervised run wires OnActivity to
// (OR-176); cmd/orion/fix.go's fixActivity only wraps it. These cases pin
// the "start" and "text" activity kinds that the fix-loop test in
// cmd/orion/actorrun_test.go doesn't exercise, so a regression there -- e.g.
// dropping the run-start marker or the agent's own narration from the event
// log -- would still be caught here even if the fix loop's own test stayed
// green.
func TestActivityLoggerEmitsRunStartOnSessionOpen(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := events.Open(logPath, events.Event{})
	if err != nil {
		t.Fatal(err)
	}

	var console strings.Builder
	activity := ActivityLogger(log, &console, "OR-176", events.ActorImplementer)
	activity(supervisor.Activity{Kind: "start", Model: "sonnet"})
	log.Close()

	logged, err := events.Read(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(logged) != 1 || logged[0].Kind != events.KindRunStart {
		t.Fatalf("events = %+v, want exactly one KindRunStart", logged)
	}
	if logged[0].Actor != events.ActorImplementer || logged[0].Model != "sonnet" {
		t.Errorf("run-start event = %+v, want actor/model attributed", logged[0])
	}
}

func TestActivityLoggerEmitsSayOnText(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	log, err := events.Open(logPath, events.Event{})
	if err != nil {
		t.Fatal(err)
	}

	var console strings.Builder
	activity := ActivityLogger(log, &console, "OR-176", events.ActorQA)
	activity(supervisor.Activity{Kind: "text", Detail: "root cause is a hand-rolled logger", Model: "sonnet"})
	log.Close()

	logged, err := events.Read(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(logged) != 1 || logged[0].Kind != events.KindSay {
		t.Fatalf("events = %+v, want exactly one KindSay", logged)
	}
	if logged[0].Actor != events.ActorQA || logged[0].Msg != "root cause is a hand-rolled logger" {
		t.Errorf("say event = %+v, want the actor and the agent's own text carried through unedited", logged[0])
	}
}
