// The run view (OR-67): renders /api/snapshot as a card grid.
//
// Class components only, no hooks -- VENDOR.md is explicit about why: the
// hooks build needs a bare "preact" specifier the browser cannot resolve
// without a bundler or an import map, and OR-66 ruled both out.
//
// No dangerouslySetInnerHTML anywhere in this file, and none is needed:
// every value below reaches the DOM as a vdom child, which htm/preact
// escape by construction (see internal/web/ui/vendor/VENDOR.md). A ticket
// summary or an activity line is text from a tracker other people can
// write to; it renders as text, never as markup, because nothing here asks
// it to be anything else.
import { render, Component } from "/vendor/preact.module.js";
import htm from "/vendor/htm.module.js";

const html = htm.bind(Component.prototype.constructor);

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

// Card is one ticket's row in the grid: everything Snapshot's Card type
// carries, and nothing this page had to invent. The mockup shows a cost
// figure the API does not -- Card has no such field, and drawing one from
// nothing would be exactly the fabrication OR-276's escaping contract and
// this whole model exist to avoid elsewhere.
class Card extends Component {
  render({ card }) {
    const s = card.Session || {};
    const active = card.Verb === "working" || card.Verb === "waiting";
    return html`
      <div class="card ${active ? "active" : ""}">
        <div class="crow">
          <span class="key">${card.Key}</span>
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
      </div>
    `;
  }
}

// App polls the snapshot endpoint rather than the stream endpoint for the
// grid itself: OR-63's stream carries individual events, and turning a
// stream of events back into "the current state of N cards" is exactly
// what the snapshot endpoint already computed server-side. The stream is
// for the log panel a later story docks here (OR-51's design reference),
// not for reshaping this grid client-side.
class App extends Component {
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

render(html`<${App} />`, document.getElementById("app"));
