// The run panel (OR-67, OR-69): the card grid over /api/snapshot and the
// docked log panel over /api/stream -- registered under the panel seam
// OR-70 added, rather than hardwired as the page's only content.
//
// Class components only, no hooks -- VENDOR.md is explicit about why: the
// hooks build needs a bare "preact" specifier the browser cannot resolve
// without a bundler or an import map, and OR-66 ruled both out.
//
// No dangerouslySetInnerHTML anywhere in this file, and none is needed:
// every value below reaches the DOM as a vdom child, which htm/preact
// escape by construction (see internal/web/ui/vendor/VENDOR.md). A ticket
// summary, an activity line, or a log message is text from a tracker or an
// agent other people can write to; it renders as text, never as markup,
// because nothing here asks it to be anything else.
import { Component, h } from "/vendor/preact.module.js";
import htm from "/vendor/htm.module.js";
import { registerPanel } from "./panels.js";

const html = htm.bind(h);

// The five verbs internal/ui renders, and no others (internal/ui/event.go).
// The browser and the terminal must not disagree about what a run looks
// like, so the icon and the word are copied from there rather than
// reinvented -- ✓/◐/⏳/⚠/✗ are the exact glyphs iconFor uses.
const ICONS = {
  ok: "✓",
  working: "◐",
  waiting: "⏳",
  warning: "⚠",
  failed: "✗",
};

function verbIcon(verb) {
  return ICONS[verb] || "○"; // iconPending: a verb this page does not recognise yet
}

// The ticket-identity palette (docs/design/web/01-run-view.html and every
// page that shares its CSS variables): a colour axis distinct from the
// five verb colours above, so a card or log line reading "this is ticket
// X" and "this run failed" never collide on one hue.
//
// NOT internal/ui's ticketColor: that assigns first-come-first-served over
// a process's lifetime (internal/ui/event.go), which needs the terminal's
// own sequential history of which ticket it saw first -- history a browser
// loading one snapshot never has. A deterministic hash of the key gives
// every card a STABLE colour across reloads and polls without needing that
// history, at the cost of not matching the terminal's own assignment for
// the same ticket in the same run. Matching that exactly would mean the
// server serving a colour per ticket, which is real scope this port did
// not take on.
const TICKET_COLORS = ["--t-orange", "--t-violet", "--t-teal", "--t-rose", "--t-sky"];

function ticketColorVar(key) {
  if (!key) return null;
  let h = 0;
  for (let i = 0; i < key.length; i++) {
    h = (h * 31 + key.charCodeAt(i)) | 0;
  }
  return TICKET_COLORS[Math.abs(h) % TICKET_COLORS.length];
}

// verbFor ports internal/ui's VerbFor (internal/ui/event.go) exactly: the
// same event Kind must map to the same verb on both surfaces, or the
// browser and the terminal disagree about what a run looks like -- the one
// thing internal/ui's own package comment says it exists to prevent.
//
// KindStage maps to "ok" here too, on purpose, matching VerbFor's own
// comment: a handoff asks nothing of the operator, so it gets the calm verb
// and a different LAYOUT (the stage row below) rather than a sixth word.
function verbFor(kind) {
  switch (kind) {
    case "failed":
    case "blocked":
      return "failed";
    case "escalate":
    case "refuse":
    case "budget":
    case "attribution":
      return "warning";
    case "ci":
      return "waiting";
    case "claimed":
    case "branch":
    case "run-start":
    case "ask":
    case "tool":
    case "say":
      return "working";
    case "note":
      return "ok";
    default:
      // answer, decision, commit, push, pr, merge, refresh, run-end, usage,
      // stage: something happened and it worked.
      return "ok";
  }
}

// fmtElapsed turns a duration in SECONDS (Go's time.Duration serialises as
// nanoseconds, but Session carries Started/Last as timestamps, not a
// duration -- this computes it client-side) into "4m12s", matching the
// mockup's own format.
function fmtElapsed(startedISO, lastISO) {
  const started = Date.parse(startedISO);
  const last = Date.parse(lastISO);
  if (!started || !last || last < started) return "";
  const total = Math.round((last - started) / 1000);
  const m = Math.floor(total / 60);
  const s = total % 60;
  return m > 0 ? `${m}m ${String(s).padStart(2, "0")}s` : `${s}s`;
}

// fmtClock renders an ISO timestamp as the log's own "13:34:48" column,
// local time -- the same format ui/stage.go's RenderStage prints with.
function fmtClock(iso) {
  const d = new Date(iso);
  if (isNaN(d)) return "";
  return d.toLocaleTimeString("en-GB", { hour12: false });
}

// Card is one ticket's row in the grid: everything Snapshot's Card type
// carries, and nothing this page had to invent. The mockup shows a cost
// figure the API does not -- Card has no such field, and drawing one from
// nothing would be exactly the fabrication OR-276's escaping contract and
// this whole model exist to avoid elsewhere.
class Card extends Component {
  render({ card }) {
    const s = card.Session || {};
    const active = card.Verb === "working" || card.Verb === "waiting";
    // The detail panel (OR-53, panel-detail.js) is reached from here: a
    // card names its own (Key, Run) pair, which is exactly what
    // /api/detail needs and what no other surface can supply. A card with
    // no Run (an event with a key but never a run id -- a supervisor line
    // before any ticket was claimed, say) has nothing to link to, so it
    // stays a plain div rather than a link that would 404.
    const href = card.Run ? `#detail/${encodeURIComponent(card.Key)}/${encodeURIComponent(card.Run)}` : null;
    const colorVar = ticketColorVar(card.Key);
    return html`
      <a href=${href} class="card ${active ? "active" : ""}" style="text-decoration:none;color:inherit;${href ? "cursor:pointer" : "cursor:default"}">
        ${colorVar ? html`<div class="rail" style="background:var(${colorVar})"></div>` : null}
        <div class="crow">
          <span class="key" style=${colorVar ? `color:var(${colorVar})` : ""}>${card.Key}</span>
          <span class="spacer" style="flex:1"></span>
          <span class="status ${card.Verb}">${verbIcon(card.Verb)} ${card.Verb}</span>
        </div>
        <div class="summary">${card.Title}</div>
        ${s.Role || s.Actor
          ? html`<div class="who">
              <span>${s.Role || s.Actor}</span>
              ${s.Model ? html`<span class="model">${s.Model}</span>` : null}
            </div>`
          : null}
        ${s.Activity ? html`<div class="activity">${s.Activity}</div>` : null}
        ${card.Gate ? html`<div class="activity">${card.Gate}</div>` : null}
        <div class="meta">
          ${s.Steps ? html`<span><b>${s.Steps}</b> steps</span>` : null}
          ${s.Started
            ? html`<span><b>${fmtElapsed(s.Started, s.Last)}</b></span>`
            : null}
        </div>
      </a>
    `;
  }
}

// CardGrid draws the run's cards, or the two states that are not a grid:
// loading, and nothing has ever run.
class CardGrid extends Component {
  render({ snapshot, error }) {
    if (error) {
      return html`<div class="empty">could not reach the server: ${error}</div>`;
    }
    if (!snapshot) {
      return html`<div class="empty">loading&hellip;</div>`;
    }
    const cards = snapshot.Cards || [];
    if (cards.length === 0) {
      // A MACHINE WHERE NOTHING HAS EVER RUN IS A NORMAL STATE (OR-65), and
      // that rule extends to what this page draws for it: an explicit
      // sentence, not a blank grid a reader has to guess the meaning of.
      return html`<div class="empty">nothing is running</div>`;
    }
    const done = cards.filter((c) => c.Session && c.Session.Done).length;
    const active = cards.length - done;
    return html`
      <div>
        <div class="head">
          <div class="h1">Running ${cards.length} agent${cards.length === 1 ? "" : "s"}</div>
          <div class="sub">${done} done &middot; ${active} active</div>
        </div>
        <div class="cards">
          ${cards.map((c) => html`<${Card} key=${c.Key} card=${c} />`)}
        </div>
      </div>
    `;
  }
}

// The mockup's own chip set: the five verbs plus "trace" -- the one word on
// the panel that is not a verb at all.
const VERBS = ["ok", "working", "waiting", "warning", "failed"];

// FilterBar is the row of chips. A single click toggles one verb in or out
// of the active set; "all" is shorthand for "every verb", not a member of
// the set itself, so it lights up exactly when the set already equals
// every verb rather than carrying its own boolean.
class FilterBar extends Component {
  render({ active, trace, onToggle, onToggleTrace, onSetAll }) {
    const allOn = VERBS.every((v) => active.has(v));
    return html`
      <div class="filter">
        <button class="f ${allOn ? "on" : ""}" onClick=${() => onSetAll(!allOn)}>
          all
        </button>
        ${VERBS.map(
          (v) => html`
            <button
              key=${v}
              class="f ${active.has(v) ? "on" : ""}"
              onClick=${() => onToggle(v)}
            >
              ${v}
            </button>
          `
        )}
        <button class="f ${trace ? "on" : ""}" onClick=${onToggleTrace}>trace</button>
      </div>
    `;
  }
}

// LogLine is one ordinary event: the six-column layout the mockup and
// ui/stage.go's own RenderStage both use -- time, ticket, verb+icon, actor,
// model, message.
//
// "trace" IS A DISPLAY FILTER, NOT A SERVER KIND. There is no events.Kind
// named trace; the mockup's chip means "the noisy, high-volume kinds" --
// tool calls and an agent narrating what it is doing (KindTool, KindSay).
// Filtered here rather than requested from the server with a different
// query param, because /api/stream's own filters (OR-63) are key and actor,
// and adding a third kind of filter to the wire contract for one chip this
// page draws would be a server change this ticket's done-when never asked
// for.
class LogLine extends Component {
  render({ e }) {
    return html`
      <div class="ln">
        <span class="ts">${fmtClock(e.at)}</span>
        <span class="lk">${e.key || ""}</span>
        <span class="lv ${e.verb}">${verbIcon(e.verb)} ${e.verb}</span>
        <span class="la">${e.actor || ""}</span>
        <span class="lm">${e.model || ""}</span>
        <span class="lg">${e.msg || ""}</span>
      </div>
    `;
  }
}

// StageRow is a stage boundary, laid out the way ui/stage.go's RenderStage
// prints one on the terminal: the same leading time+ticket columns, then
// "══ stage ══ from → to", with the handoff sentence on the line under it
// rather than crowded onto the same row -- dropping the verb/actor/model
// columns is what makes a handoff findable by eye among ordinary lines,
// which is this subtask's own stated reason for rendering it differently.
class StageRow extends Component {
  render({ e }) {
    const from = e.detail && e.detail.from;
    const to = e.detail && e.detail.to;
    const by = e.detail && e.detail.by;
    const next = e.detail && e.detail.next;
    // by/next are raw actor identifiers, not display names: actors.Display
    // resolves a name from the operator's own roster config
    // (internal/actors), which lives server-side and is not part of the
    // JSON this page reads. Showing the identifier is the honest answer for
    // what the API actually sends, not a client-side guess at a name.
    const clause = by && next ? (by === next ? `${by} continues` : `${by} hands to ${next}`) : "";
    return html`
      <div>
        <div class="stagerow">
          <span class="ts">${fmtClock(e.at)}</span>
          <span class="lk">${e.key || ""}</span>
          <span class="rule">══ stage ══</span>
          <span class="stagenames">${from} → ${to}</span>
        </div>
        ${clause
          ? html`<div class="ln"><span class="ts"></span><span class="lk"></span><span class="lv"></span>
              <span class="la"></span><span class="lm"></span>
              <span class="lg dim">${clause}</span></div>`
          : null}
      </div>
    `;
  }
}

// maxLines bounds how many lines this panel keeps in memory and on the DOM.
// A long-running watch prints for hours; without a cap the tab's memory
// grows without limit for the whole session, which is a worse failure than
// scrolling a live tail ever needs to guard against.
const maxLines = 2000;

// LogPanel owns the SSE connection and the four behaviours OR-69's
// done-when names: streamed lines, verb+trace filters, auto-scroll, and
// pause-on-scroll-up.
class LogPanel extends Component {
  constructor() {
    super();
    this.state = {
      lines: [],
      active: new Set(VERBS),
      trace: true,
      paused: false,
      connected: false,
    };
    this.linesRef = null;
  }

  componentDidMount() {
    this.connect();
  }

  componentWillUnmount() {
    if (this.source) this.source.close();
  }

  connect() {
    // No key= or actor= query param: this panel is the whole machine's log,
    // matching the card grid's own scope -- every workspace /api/snapshot
    // draws cards for, /api/stream already fans events in from all of them
    // (OR-63).
    const source = new EventSource("/api/stream");
    this.source = source;
    source.onopen = () => this.setState({ connected: true });
    source.onerror = () => this.setState({ connected: false });
    source.onmessage = (msg) => {
      let e;
      try {
        e = JSON.parse(msg.data);
      } catch {
        return; // a malformed frame is dropped, not a reason to stop the tail
      }
      const line = {
        at: e.at,
        key: e.key,
        actor: e.actor,
        model: e.model,
        msg: e.msg,
        kind: e.kind,
        verb: verbFor(e.kind),
        stage: e.kind === "stage",
        detail: e.detail,
      };
      this.setState((s) => {
        const lines = s.lines.length >= maxLines ? s.lines.slice(1) : s.lines.slice();
        lines.push(line);
        return { lines };
      }, this.maybeScroll);
    };
  }

  maybeScroll() {
    // PAUSE ON SCROLL-UP: if the reader is not pinned to the bottom, a new
    // line must not yank the view back down out from under them -- that is
    // the entire reason this control exists. Only when paused is false, or
    // the panel is not mounted yet, does a new line follow the tail.
    if (this.state.paused || !this.linesRef) return;
    this.linesRef.scrollTop = this.linesRef.scrollHeight;
  }

  onScroll() {
    if (!this.linesRef) return;
    const atBottom =
      this.linesRef.scrollHeight - this.linesRef.scrollTop - this.linesRef.clientHeight < 4;
    // Scrolling UP pauses; returning to the bottom resumes -- the mockup's
    // own control is a manual override (below) as well, so this only ever
    // turns pause ON automatically, never off, matching "pause ON
    // scroll-up" rather than a two-way auto toggle a reader did not ask for.
    if (!atBottom && !this.state.paused) {
      this.setState({ paused: true });
    }
  }

  toggleVerb(v) {
    this.setState((s) => {
      const active = new Set(s.active);
      if (active.has(v)) active.delete(v);
      else active.add(v);
      return { active };
    });
  }

  setAll(on) {
    this.setState({ active: on ? new Set(VERBS) : new Set() });
  }

  togglePause() {
    this.setState(
      (s) => ({ paused: !s.paused }),
      () => {
        if (!this.state.paused) this.maybeScroll();
      }
    );
  }

  visible() {
    return this.state.lines.filter((l) => {
      if (l.stage) return true; // a handoff is never filtered by verb or trace
      if (!this.state.trace && (l.kind === "tool" || l.kind === "say")) return false;
      return this.state.active.has(l.verb);
    });
  }

  render(_, { lines, active, trace, paused, connected }) {
    const shown = this.visible();
    return html`
      <div class="log">
        <div class="logtop">
          <div class="t">log</div>
          <span class="dim">&middot; ${connected ? "live" : "reconnecting&hellip;"}</span>
          <span class="spacer" style="flex:1"></span>
          <span class="chip">all tickets</span>
        </div>
        <${FilterBar}
          active=${active}
          trace=${trace}
          onToggle=${(v) => this.toggleVerb(v)}
          onToggleTrace=${() => this.setState((s) => ({ trace: !s.trace }))}
          onSetAll=${(on) => this.setAll(on)}
        />
        <div class="lines" ref=${(el) => (this.linesRef = el)} onScroll=${() => this.onScroll()}>
          ${shown.map((l, i) =>
            l.stage
              ? html`<${StageRow} key=${i} e=${l} />`
              : html`<${LogLine} key=${i} e=${l} />`
          )}
        </div>
        <div class="logfoot">
          <span>${paused ? "paused" : "following"} &middot; ${lines.length} line${lines.length === 1 ? "" : "s"}</span>
          <button class="pausebtn ${paused ? "paused" : ""}" onClick=${() => this.togglePause()}>
            ${paused ? "resume" : "pause on scroll-up"}
          </button>
        </div>
      </div>
    `;
  }
}

// RunPanel is the whole page body: the card grid and the log panel docked
// beside it. This is what registerPanel hands the shell.
//
// THE GRID POLLS THE SNAPSHOT ENDPOINT rather than the stream: OR-63's
// stream carries individual events, and turning a stream of events back
// into "the current state of N cards" is exactly what the snapshot
// endpoint already computed server-side. The log panel is the one surface
// that genuinely needs the stream -- a tail of individual lines is what it
// draws.
class RunPanel extends Component {
  constructor() {
    super();
    this.state = { snapshot: null, error: null };
  }

  componentDidMount() {
    this.poll();
    this.timer = setInterval(() => this.poll(), 3000);
  }

  componentWillUnmount() {
    clearInterval(this.timer);
  }

  poll() {
    fetch("/api/snapshot")
      .then((r) => {
        if (!r.ok) throw new Error(`snapshot: ${r.status}`);
        return r.json();
      })
      .then((snapshot) => this.setState({ snapshot, error: null }))
      .catch((err) => this.setState({ error: String(err) }));
  }

  render(_, { snapshot, error }) {
    return html`
      <div class="body">
        <div class="main">
          <${CardGrid} snapshot=${snapshot} error=${error} />
        </div>
        <${LogPanel} />
      </div>
    `;
  }
}

registerPanel("run", "run", RunPanel);
