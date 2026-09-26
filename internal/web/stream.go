// The live stream endpoint (OR-63): what `GET /api/stream` pushes.
//
// SERVER-SENT EVENTS, not a WebSocket. The traffic is one-directional --
// Orion tells the page what happened, the page never talks back -- and SSE
// is plain HTTP: no upgrade handshake, no extra dependency, and it survives
// the loopback-only posture server.go already commits to (OR-60) without
// adding a second protocol to that surface's threat model.
//
// EVERY WORKSPACE, FANNED IN. The snapshot endpoint (OR-62) draws the whole
// machine's cards from every workspace sessions.Scan finds; the stream
// endpoint owes the same promise for the log panel underneath them. One
// goroutine follows one workspace's log each, all writing into one channel
// the handler drains -- the shape a blocking, per-file events.Follow forces
// on anything that wants to watch more than one file from a single request.
package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/orion-sdlc/orion/internal/events"
	"github.com/orion-sdlc/orion/internal/sessions"
	"github.com/orion-sdlc/orion/internal/workspace"
)

func init() { HandleReadOnly("/api/stream", http.HandlerFunc(streamHandler)) }

// followInterval is how often Follow re-checks a log for new lines. Short
// enough that a line reaches the page while whoever is watching still
// remembers writing it; long enough that N workspaces polling in parallel do
// not become their own CPU cost.
const followInterval = 200 * time.Millisecond

// streamHandler pushes every new event, across every known workspace, to one
// connected client as Server-Sent Events.
//
// ?key= and ?actor= FILTER WHAT REACHES THE WIRE, not what Follow reads.
// Every workspace is still followed -- an event on a filtered-out ticket
// still has to be seen to be rejected -- but only a match is written to the
// client. Filtering earlier, at the log, would mean one Follow goroutine per
// filter combination instead of one per workspace, for no reader anywhere
// that needs it.
func streamHandler(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	wss, err := sessions.Scan(workspace.Home())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	keyFilter := r.URL.Query().Get("key")
	actorFilter := r.URL.Query().Get("actor")

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// THE STOP CHANNEL IS THE DISCONNECT TEST'S WHOLE ANSWER (OR-63's third
	// clause). r.Context() is cancelled by net/http itself the moment the
	// client goes away -- a closed connection, a browser tab closed, the
	// stream Response object GC'd on the client. Every Follow goroutine below
	// shares this one channel, so one disconnect stops all of them: there is
	// no per-goroutine cleanup to forget, because there is no per-goroutine
	// stop signal to forget it on.
	stop := r.Context().Done()

	out := make(chan events.Event)
	var wg sync.WaitGroup
	for _, ws := range wss {
		wg.Add(1)
		go followWorkspace(ws.Dir, stop, out, &wg)
	}
	// Closes out once every follower has returned -- which happens either on
	// disconnect (stop fires, every Follow returns) or if this handler is
	// ever asked to serve a process that is shutting down. The loop below
	// then ends on channel close rather than blocking forever on a source
	// that will never write again.
	go func() { wg.Wait(); close(out) }()

	for {
		select {
		case <-stop:
			return
		case e, ok := <-out:
			if !ok {
				return
			}
			if keyFilter != "" && e.Key != keyFilter {
				continue
			}
			if actorFilter != "" && e.Actor != actorFilter {
				continue
			}
			b, err := json.Marshal(e)
			if err != nil {
				// Malformed only if Event itself stopped being JSON-safe --
				// nothing in a live stream should abort over one bad frame
				// when the rest of the log is fine.
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
	}
}

// followWorkspace watches one workspace's log from its current end and
// writes every new event onto out, until stop fires.
//
// FROM THE CURRENT END, not from the start. A client connecting to the
// stream already has the snapshot (OR-62) for everything that already
// happened; replaying the whole log here would duplicate it and, on a log of
// any real size, delay the first genuinely new line behind everything old.
//
// WAITS FOR THE LOG TO EXIST, rather than giving up the moment it does not.
// A workspace with no log yet is not a dead end: `orion init` may have just
// created the directory, and its first event can be written any moment after
// this stream connects. Giving up on os.Stat's first failure -- the earlier
// version of this function -- meant a client that connected before an agent
// wrote its first line NEVER saw that workspace at all, for the life of the
// connection. Polling for the file the same way Follow polls for new lines
// keeps that promise instead: this goroutine is watching for the exact
// moment there is something to follow.
func followWorkspace(wsDir string, stop <-chan struct{}, out chan<- events.Event, wg *sync.WaitGroup) {
	defer wg.Done()

	path := events.Path(wsDir)
	fi, err := os.Stat(path)
	from := int64(0)
	if err == nil {
		// The log already existed when this stream connected: start from its
		// current end, because the client already has everything up to here
		// from the snapshot endpoint (OR-62) and replaying it would
		// duplicate what the page already drew.
		from = fi.Size()
	} else {
		// No log yet. Wait for one to appear rather than giving up -- a
		// workspace `orion init` just created, or one that has never run
		// anything, can start logging at any moment after this connects.
		//
		// FROM ZERO ONCE IT APPEARS, not from whatever size it has grown to
		// by the time this notices it exists. The moment a log is created
		// and the moment this goroutine's next poll observes that are not
		// the same moment -- by the time os.Stat succeeds, an event written
		// in between is already inside the file's reported size, and
		// starting from that size would silently skip it. Starting from
		// zero risks one duplicate against a snapshot fetched a moment
		// earlier; starting from the observed size risks losing the event
		// outright, which is the wrong side of that trade for a live view.
		for {
			select {
			case <-stop:
				return
			case <-time.After(followInterval):
			}
			if _, statErr := os.Stat(path); statErr == nil {
				break
			}
		}
	}

	_ = events.Follow(path, from, followInterval, stop, func(e events.Event) {
		select {
		case out <- e:
		case <-stop:
		}
	})
}
