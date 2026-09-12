// The ticket-detail panel (OR-53, OR-76, OR-79, OR-448): what happened on
// one run, end to end -- docs/design/web/03-ticket-detail.html is the
// original reference. Reached by clicking a card in the run view, never a
// nav item of its own: #detail/<key>/<run> is a destination, not a page
// someone opens cold.
//
// OR-448 REPLACES THE FLAT LOG DUMP WITH THE REAL STAGE PIPELINE AND ASK
// EXCHANGE, using the same real /api/detail data this page already fetched
// (Detail.Stages, Detail.Asks[].Turns -- detail.go's own comments state
// what each field means and when it is absent). panel-stageflow.js and
// panel-askbroker.js remain as their own separate, still-static mockup
// pages (docs/design/web/07 and 09) -- this page does not import or reuse
// them, because their layouts assume data those mockups baked in by hand
// (fixed 2-lane swim lanes, per-fan-out-child cost bars) that the real log
// does not carry in that shape. What IS reused is the .sf-/.askb- CSS
// vocabulary those two files already ship, so this reads visually
// consistent with them without cloning their fixed-scenario layout.
//
// THE RAW LOG (Steps) IS A DRILL-DOWN, NOT THE DEFAULT. This is the exact
// complaint OR-448 exists to fix: clicking a ticket used to show a flat
// terminal-style table first. It is still here, verbatim, one click away.
//
// ABSENT, NOT EMPTY, the same rule detail.go's own comment states server
// side: a run with no asks renders no "asks" section at all, not an empty
// one a reader has to know means nothing.
//
// THE DECISION TEXT IS THE LOG'S OWN LINE, VERBATIM. detail.go's own
// comment explains why: the log carries a decision as one formatted
// message, not structured fields, and parsing a path out of it risks
// silently breaking the moment work.go's message format changes. Shown
// as-is here for the same reason it is served as-is.
//
// Class components only, no hooks -- see panel-run.js for why.
import { Component, h } from "/vendor/preact.module.js";
import htm from "/vendor/htm.module.js";
import { registerPanel } from "./panels.js";

const html = htm.bind(h);

function fmtCost(usd) {
  if (!usd) return null;
  return `$${usd.toFixed(2)}`;
}

function fmtClock(iso) {
  const d = new Date(iso);
  if (isNaN(d)) return "";
  return d.toLocaleTimeString("en-GB", { hour12: false });
}

// parseHash reads "#detail/<key>/<run>" -- both segments are opaque ids
// this page never interprets, only forwards to the fetch query string
// (URLSearchParams handles the encoding, so a run id or key containing a
// character that would otherwise need escaping is still sent correctly).
function parseHash() {
  const parts = location.hash.replace(/^#/, "").split("/");
  return { key: parts[1] || "", run: parts[2] || "" };
}

class StepRow extends Component {
  render({ step }) {
    return html`
      <tr>
        <td class="desig">${fmtClock(step.At)}</td>
        <td class="nm">${step.Actor}</td>
        <td>${step.Model || html`<span class="dash">—</span>`}</td>
        <td class="why" style="max-width:600px;-webkit-line-clamp:2">${step.Text}</td>
      </tr>
    `;
  }
}

// TurnRow is one step of an ask's real exchange (routed / escalated /
// answered / refused) -- Turns is however many the log actually recorded
// (one answer, or a route+escalate+answer, per internal/work's consult()),
// never a fixed two-lane layout the way panel-askbroker.js's own mockup
// draws exactly one hardcoded scenario.
class TurnRow extends Component {
  render({ turn }) {
    const label =
      turn.Kind === "note"
        ? "routed"
        : turn.Kind === "escalate"
        ? "escalated"
        : turn.Kind === "refuse"
        ? "refused"
        : "answered";
    return html`
      <div class="ln" style="white-space:normal">
        <span class="ts">${fmtClock(turn.At)}</span>
        <span class="nm" style="width:110px">${turn.Actor}</span>
        <span class="lv ${turn.Kind === "refuse" ? "warning" : turn.Kind === "escalate" ? "warning" : "ok"}"
          >${label}</span
        >
        <span class="why" style="max-width:480px">${turn.Text}</span>
      </div>
    `;
  }
}

class AskRow extends Component {
  render({ ask }) {
    const state = ask.Refused ? "refused" : ask.Answer ? "answered" : "open";
    const turns = ask.Turns || [];
    return html`
      <div class="card">
        <div class="crow">
          <span class="status ${ask.Refused ? "failed" : ask.Answer ? "ok" : "waiting"}">${state}</span>
        </div>
        <div class="summary">${ask.Question}</div>
        ${turns.length > 0
          ? html`<div style="margin-top:8px;display:flex;flex-direction:column;gap:2px">
              ${turns.map((t, i) => html`<${TurnRow} key=${i} turn=${t} />`)}
            </div>`
          : ask.Answer
          ? html`<div class="activity">${ask.Answer}</div>`
          : null}
      </div>
    `;
  }
}

// StageNode is one stop on the pipeline spine: reused CSS from
// panel-stageflow.js's own mockup (.sf-*), fed real Detail.Stages instead
// of that page's hardcoded OR-272 sample. sf-done/sf-now colour the orb the
// same way that mockup does; there is no sf-skip/sf-todo here because a
// real run has not recorded a stage it has not reached yet -- unlike the
// mockup, which draws the whole imagined pipeline including stages still
// to come, this only draws what the log actually crossed.
class StageNode extends Component {
  render({ stage, last }) {
    const cost = fmtCost(stage.Cost);
    return html`
      <div class="sf-node">
        <div class="sf-orb ${stage.Done ? "sf-done" : "sf-now"}">${stage.Done ? "✓" : html`<span class="sf-spin"></span>◐`}</div>
        <div class="sf-nname ${stage.Done ? "sf-done" : "sf-now"}">${stage.Name}</div>
        <div class="sf-nwho">${stage.Actor}${cost ? html`<br />${cost}` : null}</div>
      </div>
      ${!last ? html`<div class="sf-link ${stage.Done ? "sf-done" : ""}"></div>` : null}
    `;
  }
}

// StageFlow is the pipeline spine for one run, built from real
// Detail.Stages -- one node per stage crossing this run actually made, in
// the order it made them. Absent (not an empty box) when Stages is nil:
// see the Detail.Stages field comment for when that is (the run has not
// handed off from wherever it started).
class StageFlow extends Component {
  render({ stages, cost }) {
    if (!stages || stages.length === 0) return null;
    const total = fmtCost(cost);
    return html`
      <div class="sect">Stage flow${total ? html` <span class="dim">· ${total} so far</span>` : null}</div>
      <div class="sf-flow" style="padding:4px 0 10px">
        ${stages.map((s, i) => html`<${StageNode} key=${i} stage=${s} last=${i === stages.length - 1} />`)}
      </div>
    `;
  }
}

class DetailPanel extends Component {
  constructor() {
    super();
    // showLog is OFF by default: this is OR-448's own fix -- a click used
    // to land on the raw Steps table first, which is the "shows logs like
    // a terminal" complaint the ticket exists to resolve. The table is
    // still here, one click away, never removed.
    this.state = { detail: null, error: null, key: "", run: "", showLog: false };
  }

  componentDidMount() {
    this.onHashChange = () => this.load();
    window.addEventListener("hashchange", this.onHashChange);
    this.load();
  }

  componentWillUnmount() {
    window.removeEventListener("hashchange", this.onHashChange);
  }

  load() {
    const { key, run } = parseHash();
    this.setState({ key, run, detail: null, error: null, showLog: false });
    if (!key || !run) {
      this.setState({ error: "no ticket selected" });
      return;
    }
    const q = new URLSearchParams({ key, run });
    fetch(`/api/detail?${q}`)
      .then((r) => {
        if (!r.ok) throw new Error(`detail: ${r.status}`);
        return r.json();
      })
      .then((detail) => this.setState({ detail, error: null }))
      .catch((err) => this.setState({ error: String(err) }));
  }

  render(_, { detail, error, key, showLog }) {
    if (error) {
      return html`<div class="empty">${error}</div>`;
    }
    if (!detail) {
      return html`<div class="empty">loading…</div>`;
    }
    const steps = detail.Steps || [];
    const asks = detail.Asks || [];
    const decisions = detail.Decisions || [];
    const stages = detail.Stages || [];
    return html`
      <div class="main">
        <div class="head">
          <div class="h1">${key}</div>
          <div class="sub"><a href="#run">← back to run</a></div>
        </div>
        <div class="scroll">
          <${StageFlow} stages=${stages} cost=${detail.Cost} />

          ${detail.PR
            ? html`
                <div class="sect">Pull request</div>
                <div class="card">
                  <div class="summary"><a href=${detail.PR.URL} target="_blank" rel="noopener">${detail.PR.URL}</a></div>
                  ${detail.PR.CI ? html`<div class="activity">CI: ${detail.PR.CI}</div>` : null}
                </div>
              `
            : null}

          ${asks.length > 0
            ? html`
                <div class="sect">Questions</div>
                ${asks.map((ask, i) => html`<${AskRow} key=${i} ask=${ask} />`)}
              `
            : null}

          ${decisions.length > 0
            ? html`
                <div class="sect">Decisions</div>
                ${decisions.map(
                  (d, i) => html`<div class="card" key=${i}><div class="summary">${d.Text}</div></div>`
                )}
              `
            : null}

          <div class="sect" style="cursor:pointer" onClick=${() => this.setState((s) => ({ showLog: !s.showLog }))}>
            ${showLog ? "▾" : "▸"} Raw log${steps.length > 0 ? html` <span class="dim">· ${steps.length} step${steps.length === 1 ? "" : "s"}</span>` : null}
          </div>
          ${showLog
            ? steps.length > 0
              ? html`
                  <table>
                    <tr><th>Time</th><th>Actor</th><th>Model</th><th>What</th></tr>
                    ${steps.map((s, i) => html`<${StepRow} key=${i} step=${s} />`)}
                  </table>
                `
              : html`<div class="empty">no steps recorded</div>`
            : null}
        </div>
      </div>
    `;
  }
}

registerPanel("detail", "detail", DetailPanel, false);
