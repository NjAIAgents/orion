// The history panel (OR-54, OR-82, OR-83): past runs, newest first, per
// workspace -- what /api/history's HistoryRow list already computed
// server-side (stage, duration, exit code, stop reason from task.json's
// own Runs field).
//
// UNREADABLE IS ITS OWN ROW, NOT A GAP. A workspace whose task.json could
// not be parsed shows as unreadable rather than silently vanishing from the
// list -- matching the server's own done-when for this endpoint.
//
// COST FOR THE WINDOW IS NOT SHOWN HERE. /api/history does not carry it
// (OR-82's own gap, documented on the ticket): the budget ledger's percent-
// of-limit figure needs a per-project orion.json this multi-workspace
// surface has no way to select. Left out rather than guessed at.
//
// Class components only, no hooks -- see panel-run.js for why.
import { Component, h } from "/vendor/preact.module.js";
import htm from "/vendor/htm.module.js";
import { registerPanel } from "./panels.js";

const html = htm.bind(h);

// fmtDuration turns RunRec.seconds (a float) into the same "4m 12s" shape
// panel-run.js's fmtElapsed uses, so the two surfaces agree on how a
// duration reads.
function fmtDuration(seconds) {
  const total = Math.round(seconds);
  if (total <= 0) return "0s";
  const m = Math.floor(total / 60);
  const s = total % 60;
  return m > 0 ? `${m}m ${String(s).padStart(2, "0")}s` : `${s}s`;
}

// fmtClock renders an ISO timestamp the same way panel-run.js's fmtClock
// does for the log panel, so a run's start time reads identically on both
// surfaces.
function fmtClock(iso) {
  const d = new Date(iso);
  if (isNaN(d)) return "";
  return d.toLocaleString("en-GB", { hour12: false });
}

// RunRow is one run: stage, when it started, how long it took, how it
// ended. exit_code 0 reads as ok; anything else reads as failed -- the
// verb vocabulary this page borrows rather than reinvents, matching
// cards.go's own five-word set even though this row only ever needs two of
// them.
class RunRow extends Component {
  render({ run }) {
    const ok = run.exit_code === 0;
    return html`
      <tr>
        <td class="nm">${run.stage}</td>
        <td class="desig">${fmtClock(run.started_at)}</td>
        <td>${fmtDuration(run.seconds)}</td>
        <td><span class="status ${ok ? "ok" : "failed"}">${ok ? "0" : run.exit_code}</span></td>
        <td class="why">${run.reason}</td>
      </tr>
    `;
  }
}

class WorkspaceSection extends Component {
  render({ row }) {
    if (row.Unreadable) {
      return html`
        <div class="sect">${row.Key || row.WorkspaceID}</div>
        <div class="empty">task.json could not be read for this workspace</div>
      `;
    }
    const runs = row.Runs || [];
    if (runs.length === 0) {
      return html`
        <div class="sect">${row.Key || row.WorkspaceID}</div>
        <div class="empty">no runs recorded</div>
      `;
    }
    return html`
      <div class="sect">${row.Key || row.WorkspaceID}</div>
      <table>
        <tr><th>Stage</th><th>Started</th><th>Duration</th><th>Exit</th><th>Reason</th></tr>
        ${runs.map((run, i) => html`<${RunRow} key=${i} run=${run} />`)}
      </table>
    `;
  }
}

class HistoryPanel extends Component {
  constructor() {
    super();
    this.state = { rows: null, error: null };
  }

  componentDidMount() {
    fetch("/api/history")
      .then((r) => {
        if (!r.ok) throw new Error(`history: ${r.status}`);
        return r.json();
      })
      .then((rows) => this.setState({ rows, error: null }))
      .catch((err) => this.setState({ error: String(err) }));
  }

  render(_, { rows, error }) {
    if (error) {
      return html`<div class="empty">could not reach the server: ${error}</div>`;
    }
    if (!rows) {
      return html`<div class="empty">loading…</div>`;
    }
    if (rows.length === 0) {
      // A MACHINE WHERE NOTHING HAS EVER RUN IS A NORMAL STATE, the same
      // rule OR-65 states for the card grid, restated here for history.
      return html`
        <div class="main">
          <div class="head"><div class="h1">History</div></div>
          <div class="empty">nothing has run yet</div>
        </div>
      `;
    }
    return html`
      <div class="main">
        <div class="head">
          <div class="h1">History</div>
          <div class="sub">past runs, newest first, per workspace</div>
        </div>
        <div class="scroll">
          ${rows.map((row) => html`<${WorkspaceSection} key=${row.WorkspaceID} row=${row} />`)}
        </div>
      </div>
    `;
  }
}

registerPanel("history", "history", HistoryPanel);
