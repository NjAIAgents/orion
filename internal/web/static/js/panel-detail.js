// The ticket-detail panel (OR-53, OR-76, OR-79): what happened on one run,
// end to end -- docs/design/web/03-ticket-detail.html is the reference.
// Reached by clicking a card in the run view, never a nav item of its own:
// #detail/<key>/<run> is a destination, not a page someone opens cold.
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
        <td>${step.Model || html`<span class="dash">&mdash;</span>`}</td>
        <td class="why" style="max-width:600px;-webkit-line-clamp:2">${step.Text}</td>
      </tr>
    `;
  }
}

class AskRow extends Component {
  render({ ask }) {
    const state = ask.Refused ? "refused" : ask.Answer ? "answered" : "open";
    return html`
      <div class="card">
        <div class="crow">
          <span class="status ${ask.Refused ? "failed" : ask.Answer ? "ok" : "waiting"}">${state}</span>
        </div>
        <div class="summary">${ask.Question}</div>
        ${ask.Answer ? html`<div class="activity">${ask.Answer}</div>` : null}
      </div>
    `;
  }
}

class DetailPanel extends Component {
  constructor() {
    super();
    this.state = { detail: null, error: null, key: "", run: "" };
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
    this.setState({ key, run, detail: null, error: null });
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

  render(_, { detail, error, key }) {
    if (error) {
      return html`<div class="empty">${error}</div>`;
    }
    if (!detail) {
      return html`<div class="empty">loading&hellip;</div>`;
    }
    const steps = detail.Steps || [];
    const asks = detail.Asks || [];
    const decisions = detail.Decisions || [];
    return html`
      <div class="main">
        <div class="head">
          <div class="h1">${key}</div>
          <div class="sub"><a href="#run">&larr; back to run</a></div>
        </div>
        <div class="scroll">
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

          ${steps.length > 0
            ? html`
                <div class="sect">Steps</div>
                <table>
                  <tr><th>Time</th><th>Actor</th><th>Model</th><th>What</th></tr>
                  ${steps.map((s, i) => html`<${StepRow} key=${i} step=${s} />`)}
                </table>
              `
            : html`<div class="empty">no steps recorded</div>`}
        </div>
      </div>
    `;
  }
}

registerPanel("detail", "detail", DetailPanel, false);
