package main

// `orion dashboard`: is the coding queue outrunning the integration queue?
//
// A command rather than a service (OR-254). The question is asked when
// somebody wonders why READY keeps growing, not continuously, and a web
// surface would be a second thing to run and keep alive for an answer that is
// three seconds of arithmetic over a log that already exists.

import (
	"fmt"
	"os"

	"github.com/orion-sdlc/orion/internal/dashboard"
	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/registry"
	"github.com/orion-sdlc/orion/internal/workspace"
)

func runDashboard(args []string) {
	home := workspace.Home()
	key := ""
	if p := positional(args); len(p) > 0 {
		key = p[0]
	}
	ws, err := workspaceFor(home, key)
	if err != nil {
		fmt.Fprintf(os.Stderr, "orion: %v\n", err)
		os.Exit(1)
	}
	v, err := dashboard.Read(events.Path(ws.Dir))
	if err != nil {
		fmt.Fprintf(os.Stderr, "orion: reading the event log: %v\n", err)
		os.Exit(1)
	}
	dashboard.Render(os.Stdout, v)
}

// workspaceFor resolves which project's log to read.
//
// Named explicitly, or the only registered one. Guessing among several would
// answer a question about the wrong project, and the numbers would look
// plausible either way -- which is the worst kind of wrong for a dashboard.
func workspaceFor(home, key string) (*workspace.Workspace, error) {
	if key != "" {
		e, err := registry.Lookup(home, key)
		if err != nil {
			return nil, err
		}
		return workspace.Open(e.Workspace)
	}
	f, err := registry.Load(home)
	if err != nil {
		return nil, err
	}
	switch len(f.Repos) {
	case 0:
		return nil, fmt.Errorf("no project is registered; run orion plan first")
	case 1:
		for _, e := range f.Repos {
			return workspace.Open(e.Workspace)
		}
	}
	var keys []string
	for k := range f.Repos {
		keys = append(keys, k)
	}
	return nil, fmt.Errorf("several projects are registered; name one: %v", keys)
}
