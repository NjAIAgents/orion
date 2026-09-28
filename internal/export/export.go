// Author: Navjyot Nishant
// Created: 2026-09-27
// Last updated: 2026-09-27
// Description: ships Orion's events to an observability backend over OTLP or Loki push (OR-556).

// Package export ships Orion's events to the observability backend an
// operator already runs, so a watch's history can be searched and charted
// instead of living in a file per project.
//
// ONE STANDARD, NOT ONE PLUGIN PER VENDOR. Grafana Cloud, Datadog,
// Dynatrace, New Relic and Honeycomb all accept OTLP logs over HTTP, so a
// vendor is a preset -- which header carries the key -- rather than code.
// Loki's own push API is kept for a self-hosted Loki older than 3.x, which
// has no OTLP endpoint.
//
// NEVER IN THE WAY. Emit hands events over without blocking: they wait in a
// bounded buffer, go out in batches from one goroutine, and are dropped --
// with one warning -- when the buffer is full or the backend refuses them.
// Observability that can stall a watch is a new way for the watch to fail.
package export

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/orion-sdlc/orion/internal/events"
)

// Config is ~/.orion/observability.json. Machine-wide rather than per
// project: one Grafana stack serves every project a watcher runs.
//
// Secrets are never in it. Each credential is the NAME of an environment
// variable, read at start.
type Config struct {
	Enabled  bool   `json:"enabled"`
	Preset   string `json:"preset"`
	Endpoint string `json:"endpoint"`
	// UserEnv and TokenEnv override the preset's variable names.
	UserEnv  string `json:"user_env,omitempty"`
	TokenEnv string `json:"token_env,omitempty"`
	// HeadersEnv adds headers for the generic "otlp" preset: header name to
	// the environment variable holding its value.
	HeadersEnv map[string]string `json:"headers_env,omitempty"`
	// IncludeToolEvents ships every agent tool call too. Off by default:
	// one per tool call is most of the volume a backend bills for, and the
	// run log (`orion logs KEY`) already has them.
	IncludeToolEvents bool `json:"include_tool_events,omitempty"`
}

// Path is where the config lives.
func Path(home string) string { return filepath.Join(home, "observability.json") }

// Load reads the config. No file is the default: export off.
func Load(home string) (Config, error) {
	var c Config
	b, err := os.ReadFile(Path(home))
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", Path(home), err)
	}
	return c, nil
}

type format int

const (
	otlp format = iota
	loki
)

// preset is how one backend wants its key: HTTP Basic from a user and a
// token, or a named header, optionally prefixed.
type preset struct {
	format   format
	basic    bool
	header   string
	prefix   string
	userEnv  string
	tokenEnv string
}

var presets = map[string]preset{
	"otlp":      {format: otlp},
	"grafana":   {format: otlp, basic: true, userEnv: "GRAFANA_CLOUD_INSTANCE_ID", tokenEnv: "GRAFANA_CLOUD_TOKEN"},
	"loki":      {format: loki, basic: true, userEnv: "LOKI_USER", tokenEnv: "LOKI_TOKEN"},
	"datadog":   {format: otlp, header: "DD-API-KEY", tokenEnv: "DD_API_KEY"},
	"newrelic":  {format: otlp, header: "api-key", tokenEnv: "NEW_RELIC_LICENSE_KEY"},
	"dynatrace": {format: otlp, header: "Authorization", prefix: "Api-Token ", tokenEnv: "DT_API_TOKEN"},
	"honeycomb": {format: otlp, header: "x-honeycomb-team", tokenEnv: "HONEYCOMB_API_KEY"},
}

// Presets lists the preset names, for messages.
func Presets() []string {
	var out []string
	for k := range presets {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Tuning. Variables so tests need not wait out real intervals.
var (
	bufferSize   = 1000
	batchSize    = 100
	flushEvery   = 5 * time.Second
	postTimeout  = 10 * time.Second
	closeTimeout = 5 * time.Second
)

// Exporter ships events in the background.
type Exporter struct {
	endpoint string
	format   format
	tools    bool
	headers  map[string]string
	client   *http.Client
	ch       chan events.Event
	warn     func(string)
	done     chan struct{}
	stop     chan struct{}

	mu      sync.Mutex
	dropped bool // a full buffer has been reported
	failing bool // the backend is refusing, and that has been reported
}

// Start resolves the config and starts the shipper. An error means export
// cannot run -- a missing variable, an unknown preset -- and says exactly
// what to fix; the caller reports it and carries on without export.
func Start(c Config, warn func(string)) (*Exporter, error) {
	p, ok := presets[c.Preset]
	if !ok {
		return nil, fmt.Errorf("observability.json: unknown preset %q (one of %v)", c.Preset, Presets())
	}
	if c.Endpoint == "" {
		return nil, fmt.Errorf("observability.json: endpoint is required for preset %q", c.Preset)
	}
	userEnv, tokenEnv := p.userEnv, p.tokenEnv
	if c.UserEnv != "" {
		userEnv = c.UserEnv
	}
	if c.TokenEnv != "" {
		tokenEnv = c.TokenEnv
	}
	headers := map[string]string{"Content-Type": "application/json"}
	need := func(name string) (string, error) {
		v := os.Getenv(name)
		if v == "" {
			return "", fmt.Errorf("observability export: %s is not set; export is off for this run", name)
		}
		return v, nil
	}
	switch {
	case p.basic:
		// A self-hosted Loki may need no auth at all: only when neither
		// variable is set, and only for loki.
		u, t := os.Getenv(userEnv), os.Getenv(tokenEnv)
		if p.format == loki && u == "" && t == "" {
			break
		}
		if _, err := need(userEnv); err != nil {
			return nil, err
		}
		if _, err := need(tokenEnv); err != nil {
			return nil, err
		}
		headers["Authorization"] = "Basic " + base64.StdEncoding.EncodeToString([]byte(u+":"+t))
	case p.header != "":
		v, err := need(tokenEnv)
		if err != nil {
			return nil, err
		}
		headers[p.header] = p.prefix + v
	}
	for h, env := range c.HeadersEnv {
		v, err := need(env)
		if err != nil {
			return nil, err
		}
		headers[h] = v
	}
	if warn == nil {
		warn = func(string) {}
	}
	e := &Exporter{
		endpoint: c.Endpoint, format: p.format, tools: c.IncludeToolEvents, headers: headers,
		client: &http.Client{Timeout: postTimeout},
		ch:     make(chan events.Event, bufferSize), warn: warn,
		done: make(chan struct{}), stop: make(chan struct{}),
	}
	go e.loop()
	return e, nil
}

// Send queues one event. Never blocks: a full buffer drops the event and
// says so once.
func (e *Exporter) Send(ev events.Event) {
	if ev.Kind == events.KindTool && !e.tools {
		return
	}
	select {
	case e.ch <- ev:
	default:
		e.mu.Lock()
		first := !e.dropped
		e.dropped = true
		e.mu.Unlock()
		if first {
			e.warn("observability export is behind; dropping events until it catches up")
		}
	}
}

// Close flushes what is buffered, waiting at most closeTimeout.
func (e *Exporter) Close() {
	close(e.stop)
	select {
	case <-e.done:
	case <-time.After(closeTimeout):
	}
}

func (e *Exporter) loop() {
	defer close(e.done)
	t := time.NewTicker(flushEvery)
	defer t.Stop()
	var batch []events.Event
	flush := func() {
		if len(batch) > 0 {
			e.post(batch)
			batch = nil
		}
	}
	for {
		select {
		case ev := <-e.ch:
			batch = append(batch, ev)
			if len(batch) >= batchSize {
				flush()
			}
		case <-t.C:
			flush()
		case <-e.stop:
			for {
				select {
				case ev := <-e.ch:
					batch = append(batch, ev)
				default:
					flush()
					return
				}
			}
		}
	}
}

// post sends one batch. A refusal is reported once, and its recovery once;
// the batch is not retried -- a backend that is down stays down for longer
// than a buffer is worth holding.
func (e *Exporter) post(batch []events.Event) {
	var body []byte
	if e.format == loki {
		body = lokiBody(batch)
	} else {
		body = otlpBody(batch)
	}
	req, err := http.NewRequest(http.MethodPost, e.endpoint, bytes.NewReader(body))
	if err == nil {
		for k, v := range e.headers {
			req.Header.Set(k, v)
		}
		var resp *http.Response
		resp, err = e.client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 300 {
				err = fmt.Errorf("%s answered %d", e.endpoint, resp.StatusCode)
			}
		}
	}
	e.mu.Lock()
	was := e.failing
	e.failing = err != nil
	e.mu.Unlock()
	switch {
	case err != nil && !was:
		e.warn(fmt.Sprintf("observability export failed, events are being dropped: %v", scrub(err.Error())))
	case err == nil && was:
		e.warn("observability export recovered")
	}
}

// secretish matches the shapes of credential that can reach an event's
// message or detail: provider tokens, bearer headers, and a password in a URL.
var secretish = regexp.MustCompile(
	`(gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|xox[abprs]-[A-Za-z0-9-]{10,}|` +
		`sk-(ant-)?[A-Za-z0-9_-]{20,}|AKIA[0-9A-Z]{16}|glc_[A-Za-z0-9+/=]{20,}|` +
		`(?i:bearer\s+)[A-Za-z0-9._~+/-]{16,}=*|://[^\s:/@]+:[^\s@/]+@)`)

// scrub replaces anything credential-shaped before it leaves the machine.
func scrub(s string) string { return secretish.ReplaceAllString(s, "[redacted]") }

func line(ev events.Event) string {
	s := ev.Msg
	if ev.Kind != "" {
		s = ev.Kind + ": " + s
	}
	return scrub(s)
}

// attrs are the event's fields as searchable attributes. project is a
// resource attribute instead, so Loki indexes it; one label per ticket
// would explode a Loki stream count.
func attrs(ev events.Event) map[string]string {
	out := map[string]string{}
	for k, v := range map[string]string{
		"ticket": ev.Key, "actor": ev.Actor, "kind": ev.Kind,
		"run": ev.Run, "model": ev.Model,
	} {
		if v != "" {
			out[k] = v
		}
	}
	for k, v := range ev.Detail {
		out["detail."+k] = scrub(fmt.Sprint(v))
	}
	return out
}

type kv struct {
	Key   string         `json:"key"`
	Value map[string]any `json:"value"`
}

func strKV(k, v string) kv { return kv{Key: k, Value: map[string]any{"stringValue": v}} }

func sortedKVs(m map[string]string) []kv {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]kv, 0, len(keys))
	for _, k := range keys {
		out = append(out, strKV(k, m[k]))
	}
	return out
}

// otlpBody is an OTLP/HTTP JSON logs request: one resource per project.
func otlpBody(batch []events.Event) []byte {
	byProject := map[string][]events.Event{}
	var order []string
	for _, ev := range batch {
		if _, ok := byProject[ev.Project]; !ok {
			order = append(order, ev.Project)
		}
		byProject[ev.Project] = append(byProject[ev.Project], ev)
	}
	var resources []map[string]any
	for _, p := range order {
		res := []kv{strKV("service.name", "orion")}
		if p != "" {
			res = append(res, strKV("project", p))
		}
		var records []map[string]any
		for _, ev := range byProject[p] {
			records = append(records, map[string]any{
				"timeUnixNano": strconv.FormatInt(ev.At.UnixNano(), 10),
				"severityText": severity(ev),
				"body":         map[string]any{"stringValue": line(ev)},
				"attributes":   sortedKVs(attrs(ev)),
			})
		}
		resources = append(resources, map[string]any{
			"resource": map[string]any{"attributes": res},
			"scopeLogs": []map[string]any{{
				"scope":      map[string]any{"name": "orion"},
				"logRecords": records,
			}},
		})
	}
	b, _ := json.Marshal(map[string]any{"resourceLogs": resources})
	return b
}

// lokiBody is a Loki push request: one stream per project, the event's
// other fields inside the line as JSON so LogQL's json parser reads them.
func lokiBody(batch []events.Event) []byte {
	byProject := map[string][][2]string{}
	var order []string
	for _, ev := range batch {
		if _, ok := byProject[ev.Project]; !ok {
			order = append(order, ev.Project)
		}
		fields := attrs(ev)
		fields["msg"] = line(ev)
		fields["level"] = severity(ev)
		b, _ := json.Marshal(fields)
		byProject[ev.Project] = append(byProject[ev.Project],
			[2]string{strconv.FormatInt(ev.At.UnixNano(), 10), string(b)})
	}
	var streams []map[string]any
	for _, p := range order {
		labels := map[string]string{"service_name": "orion"}
		if p != "" {
			labels["project"] = p
		}
		streams = append(streams, map[string]any{"stream": labels, "values": byProject[p]})
	}
	b, _ := json.Marshal(map[string]any{"streams": streams})
	return b
}

// severity maps an event's kind onto a log level a backend can filter on.
func severity(ev events.Event) string {
	switch ev.Kind {
	case events.KindFailed:
		return "ERROR"
	case events.KindBlocked, events.KindEscalate, events.KindRefuse:
		return "WARN"
	}
	return "INFO"
}
