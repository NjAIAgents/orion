// The config panel (OR-54, OR-81): what each role runs on, and where the
// value came from -- docs/design/web/04-agents.html is the reference this
// draws from.
//
// NO "WHY THIS MODEL" COLUMN. The mockup shows one, hand-written per actor.
// This page has no such text to show: /api/config's ConfigView carries only
// what actors.Roster actually resolves (id, name, designation, model,
// effort, per-field provenance), and inventing explanatory prose here would
// be exactly the duplication OR-54's own ticket text warns against -- a
// copy that reads right the day it is written and goes quietly stale the
// first time a role's real reasoning changes.
//
// Class components only, no hooks -- see panel-run.js for why.
import { Component, h } from "/vendor/preact.module.js";
import htm from "/vendor/htm.module.js";
import { registerPanel } from "./panels.js";

const html = htm.bind(h);

// modelClass maps a model name to the badge colour the mockup defines.
// Falls through to no class (plain text) for anything else, rather than
// guessing a colour for a model this page has never been told about.
function modelClass(model) {
  switch (model) {
    case "opus":
    case "sonnet":
    case "haiku":
      return model;
    default:
      return "";
  }
}

// Row is one actor's line. overridden lists which of Name/Designation/
// Model/Effort the operator's own agents.json decided -- each gets its own
// "overridden" tag next to the value it applies to, not one blanket tag for
// the whole row, since a row can be part-shipped and part-overridden.
class Row extends Component {
  render({ entry }) {
    const o = entry.Overridden;
    const tag = (on) => (on ? html`<span class="ovr">overridden</span>` : null);
    return html`
      <tr>
        <td class="nm">
          ${entry.Name || html`<span class="dash">—</span>`}
          ${tag(o.Name)}
          <div class="id">${entry.ID}</div>
        </td>
        <td class="desig">${entry.Designation}${tag(o.Designation)}</td>
        <td>
          ${entry.Model
            ? html`<span class="mdl ${modelClass(entry.Model)}">${entry.Model}</span>`
            : html`<span class="dash">—</span>`}
          ${tag(o.Model)}
        </td>
        <td>
          ${entry.Effort || html`<span class="dash">—</span>`}
          ${tag(o.Effort)}
        </td>
      </tr>
    `;
  }
}

class ConfigPanel extends Component {
  constructor() {
    super();
    this.state = { view: null, error: null };
  }

  componentDidMount() {
    fetch("/api/config")
      .then((r) => {
        if (!r.ok) throw new Error(`config: ${r.status}`);
        return r.json();
      })
      .then((view) => this.setState({ view, error: null }))
      .catch((err) => this.setState({ error: String(err) }));
  }

  render(_, { view, error }) {
    if (error) {
      return html`<div class="empty">could not reach the server: ${error}</div>`;
    }
    if (!view) {
      return html`<div class="empty">loading…</div>`;
    }
    const roster = view.Roster || [];
    if (roster.length === 0) {
      return html`<div class="empty">no configurable roles</div>`;
    }
    return html`
      <div class="main">
        <div class="head">
          <div class="h1">Agent roster</div>
          <div class="sub">what each role runs on, and where the value came from</div>
        </div>
        <div class="scroll">
          <table>
            <tr><th>Actor</th><th>Role</th><th>Model</th><th>Effort</th></tr>
            ${roster.map((entry) => html`<${Row} key=${entry.ID} entry=${entry} />`)}
          </table>
        </div>
      </div>
    `;
  }
}

registerPanel("config", "config", ConfigPanel);
