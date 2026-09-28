package export

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orion-sdlc/orion/internal/events"
)

// capture is a fake backend that records every request.
type capture struct {
	mu     sync.Mutex
	bodies [][]byte
	heads  []http.Header
	status int
	hang   chan struct{}
}

func (c *capture) handler(w http.ResponseWriter, r *http.Request) {
	if c.hang != nil {
		<-c.hang
	}
	b, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	c.bodies = append(c.bodies, b)
	c.heads = append(c.heads, r.Header.Clone())
	st := c.status
	c.mu.Unlock()
	if st == 0 {
		st = http.StatusNoContent
	}
	w.WriteHeader(st)
}

func (c *capture) last(t *testing.T) ([]byte, http.Header) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.bodies) == 0 {
		t.Fatal("the backend received nothing")
	}
	return c.bodies[len(c.bodies)-1], c.heads[len(c.heads)-1]
}

func fakeBackend(t *testing.T) (*capture, *httptest.Server) {
	t.Helper()
	c := &capture{}
	s := httptest.NewServer(http.HandlerFunc(c.handler))
	t.Cleanup(s.Close)
	return c, s
}

func event(msg string) events.Event {
	return events.Event{At: time.Unix(1790000000, 0), Kind: events.KindFailed, Actor: "implementer",
		Project: "LTA", Key: "LTA-2", Model: "opus", Msg: msg}
}

// No file is the default, and the default is off: nothing is sent.
func TestExportIsOffWithoutAConfig(t *testing.T) {
	c, err := Load(t.TempDir())
	if err != nil || c.Enabled {
		t.Fatalf("Load with no file = %+v, %v; want disabled", c, err)
	}
}

// The Grafana Cloud preset: OTLP JSON, HTTP Basic from the instance ID and
// token variables, service.name=orion and the project as resource attributes.
func TestGrafanaPresetSendsOTLPWithBasicAuth(t *testing.T) {
	c, s := fakeBackend(t)
	t.Setenv("GRAFANA_CLOUD_INSTANCE_ID", "123456")
	t.Setenv("GRAFANA_CLOUD_TOKEN", "glc_secret")
	ex, err := Start(Config{Enabled: true, Preset: "grafana", Endpoint: s.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ex.Send(event("suite red: 3 failed"))
	ex.Close()

	body, h := c.last(t)
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("123456:glc_secret"))
	if h.Get("Authorization") != want {
		t.Fatalf("Authorization = %q, want %q", h.Get("Authorization"), want)
	}
	var req struct {
		ResourceLogs []struct {
			Resource struct {
				Attributes []kv `json:"attributes"`
			} `json:"resource"`
			ScopeLogs []struct {
				LogRecords []struct {
					SeverityText string         `json:"severityText"`
					Body         map[string]any `json:"body"`
					Attributes   []kv           `json:"attributes"`
				} `json:"logRecords"`
			} `json:"scopeLogs"`
		} `json:"resourceLogs"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("not OTLP JSON: %v\n%s", err, body)
	}
	res := attrMap(req.ResourceLogs[0].Resource.Attributes)
	if res["service.name"] != "orion" || res["project"] != "LTA" {
		t.Fatalf("resource attributes = %v", res)
	}
	rec := req.ResourceLogs[0].ScopeLogs[0].LogRecords[0]
	if rec.SeverityText != "ERROR" || !strings.Contains(rec.Body["stringValue"].(string), "suite red") {
		t.Fatalf("log record = %+v", rec)
	}
	if a := attrMap(rec.Attributes); a["ticket"] != "LTA-2" || a["actor"] != "implementer" {
		t.Fatalf("record attributes = %v", a)
	}
}

func attrMap(kvs []kv) map[string]string {
	out := map[string]string{}
	for _, a := range kvs {
		out[a.Key], _ = a.Value["stringValue"].(string)
	}
	return out
}

// A vendor is a preset: Datadog's key rides in its own header.
func TestDatadogPresetUsesItsHeader(t *testing.T) {
	c, s := fakeBackend(t)
	t.Setenv("DD_API_KEY", "dd-key")
	ex, err := Start(Config{Enabled: true, Preset: "datadog", Endpoint: s.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ex.Send(event("x"))
	ex.Close()
	if _, h := c.last(t); h.Get("DD-API-KEY") != "dd-key" || h.Get("Authorization") != "" {
		t.Fatalf("headers = %v", h)
	}
}

// Loki's push API: a stream per project labelled service_name and project,
// and the ticket inside the line, not a label.
func TestLokiPresetPushesStreams(t *testing.T) {
	c, s := fakeBackend(t)
	ex, err := Start(Config{Enabled: true, Preset: "loki", Endpoint: s.URL}, nil)
	if err != nil {
		t.Fatalf("a self-hosted Loki with no auth must start: %v", err)
	}
	ex.Send(event("landed"))
	ex.Close()
	body, _ := c.last(t)
	var req struct {
		Streams []struct {
			Stream map[string]string `json:"stream"`
			Values [][2]string       `json:"values"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	st := req.Streams[0]
	if st.Stream["service_name"] != "orion" || st.Stream["project"] != "LTA" || st.Stream["ticket"] != "" {
		t.Fatalf("labels = %v", st.Stream)
	}
	if !strings.Contains(st.Values[0][1], `"ticket":"LTA-2"`) {
		t.Fatalf("line = %s", st.Values[0][1])
	}
}

// A missing variable names itself, and export does not start.
func TestAMissingVariableIsNamed(t *testing.T) {
	t.Setenv("GRAFANA_CLOUD_TOKEN", "")
	t.Setenv("GRAFANA_CLOUD_INSTANCE_ID", "1")
	_, err := Start(Config{Enabled: true, Preset: "grafana", Endpoint: "http://x"}, nil)
	if err == nil || !strings.Contains(err.Error(), "GRAFANA_CLOUD_TOKEN") {
		t.Fatalf("err = %v, want it to name GRAFANA_CLOUD_TOKEN", err)
	}
	if _, err := Start(Config{Enabled: true, Preset: "splunkish", Endpoint: "http://x"}, nil); err == nil ||
		!strings.Contains(err.Error(), "grafana") {
		t.Fatalf("an unknown preset should list the known ones: %v", err)
	}
}

// A refusing backend is said once and its recovery once, not per batch.
func TestAFailingBackendWarnsOnceAndRecovers(t *testing.T) {
	c, s := fakeBackend(t)
	c.status = http.StatusInternalServerError
	var mu sync.Mutex
	var warns []string
	ex, err := Start(Config{Enabled: true, Preset: "otlp", Endpoint: s.URL},
		func(m string) { mu.Lock(); warns = append(warns, m); mu.Unlock() })
	if err != nil {
		t.Fatal(err)
	}
	ex.post([]events.Event{event("a")})
	ex.post([]events.Event{event("b")})
	c.mu.Lock()
	c.status = 0
	c.mu.Unlock()
	ex.post([]events.Event{event("c")})
	ex.Close()
	mu.Lock()
	defer mu.Unlock()
	if len(warns) != 2 || !strings.Contains(warns[0], "500") || !strings.Contains(warns[1], "recovered") {
		t.Fatalf("warnings = %q, want one failure and one recovery", warns)
	}
}

// Send never blocks a watch: with the backend hung and the buffer full,
// events are dropped with a single warning.
func TestSendNeverBlocks(t *testing.T) {
	c, s := fakeBackend(t)
	c.hang = make(chan struct{})
	defer close(c.hang)
	old := bufferSize
	bufferSize = 5
	defer func() { bufferSize = old }()
	var mu sync.Mutex
	drops := 0
	ex, err := Start(Config{Enabled: true, Preset: "otlp", Endpoint: s.URL},
		func(m string) {
			if strings.Contains(m, "dropping") {
				mu.Lock()
				drops++
				mu.Unlock()
			}
		})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for i := 0; i < 500; i++ {
		ex.Send(event("x"))
	}
	if time.Since(start) > time.Second {
		t.Fatalf("500 sends took %s with the backend hung", time.Since(start))
	}
	mu.Lock()
	defer mu.Unlock()
	if drops != 1 {
		t.Fatalf("dropping was reported %d times, want once", drops)
	}
}

// A credential in an event never leaves the machine.
func TestSecretsAreScrubbed(t *testing.T) {
	ev := event("push failed: https://x-access-token:ghs_A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8@github.com/o/r")
	ev.Detail = map[string]any{"auth": "Bearer abcdefghijklmnopqrstuvwxyz0123"}
	for _, body := range [][]byte{otlpBody([]events.Event{ev}), lokiBody([]events.Event{ev})} {
		if strings.Contains(string(body), "ghs_A1b2") || strings.Contains(string(body), "abcdefghijklmnop") {
			t.Fatalf("a credential reached the payload:\n%s", body)
		}
	}
}

// Tool-call events are most of the volume and are off unless asked for.
func TestToolEventsAreSkippedByDefault(t *testing.T) {
	c, s := fakeBackend(t)
	ex, err := Start(Config{Enabled: true, Preset: "otlp", Endpoint: s.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tool := event("ran ls")
	tool.Kind = events.KindTool
	ex.Send(tool)
	ex.Close()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.bodies) != 0 {
		t.Fatalf("a tool event was shipped: %s", c.bodies[0])
	}
}

// The config round-trips from ~/.orion/observability.json.
func TestLoadReadsTheFile(t *testing.T) {
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "observability.json"),
		[]byte(`{"enabled":true,"preset":"grafana","endpoint":"https://otlp-gateway-prod-us-east-0.grafana.net/otlp/v1/logs"}`), 0o600)
	c, err := Load(home)
	if err != nil || !c.Enabled || c.Preset != "grafana" {
		t.Fatalf("Load = %+v, %v", c, err)
	}
}
