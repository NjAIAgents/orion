// The ticket-detail panel (OR-53, OR-76, OR-79, OR-448): what happened on
// one run, end to end -- docs/design/web/03-ticket-detail.html is the
// original reference. Reached by clicking a card in the run view, never a
// nav item of its own: #detail/<key>/<run> is a destination, not a page
// someone opens cold.
//
// OR-448 REPLACES THE FLAT LOG DUMP WITH TWO REAL TABS -- Stage Flow (the
// pipeline spine, fan-out expansions) and Ask Broker (one node-graph
// canvas per real ask) -- built from the same /api/detail data this page
// already fetched (Detail.Stages, Detail.Asks[].Turns -- detail.go's own
// comments state what each field means and when it is absent). Both tabs
// reuse the .sf-/.askb- CSS panel-stageflow.js and panel-askbroker.js
// already ship, so this reads visually consistent with those mockups
// without being either of them: this page draws from real, variable-shape
// data (any number of stages, any number of asks, a fan of any size),
// where the two standalone mockup pages still draw one hardcoded OR-272
// scenario each and remain untouched, separate reference pages.
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
import { actorColorVar } from "./actorcolor.js";

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
    const colorVar = actorColorVar(step.Actor);
    return html`
      <tr>
        <td class="desig">${fmtClock(step.At)}</td>
        <td class="nm" style=${colorVar ? `color:var(${colorVar})` : ""}>${step.Actor}</td>
        <td>${step.Model || html`<span class="dash">—</span>`}</td>
        <td class="why" style="max-width:600px;-webkit-line-clamp:2">${step.Text}</td>
      </tr>
    `;
  }
}

// ADVISOR_SLOTS is the fixed pair internal/advise's Route can name for a
// question (internal/advise/advise.go: RoleArchitect, RolePM -- RoleDBA and
// RoleHuman are not consulted through this same ask/route/escalate path).
// Both slots are always drawn: an advisor this ticket never actually
// consulted renders dimmed rather than omitted, per the "list all, disable
// what's unused" instruction -- the canvas shows who COULD have been asked,
// not only who was.
const ADVISOR_SLOTS = [
  { actor: "architect", title: "architect", cls: "askb-arch" },
  { actor: "pm", title: "product manager", cls: "askb-pm" },
];

// AskBroker is one ask's real routing/escalation/answer exchange, drawn as
// the node graph panel-askbroker.js's own mockup (docs/design/web/09)
// already draws for its one hardcoded scenario -- fixed node positions
// (the mockup's own layout), but which advisor lights up, what each node's
// footer says, and whether a "refused" or "answering" verdict shows all
// come from this ask's real Turns. An advisor never mentioned in Turns
// stays in its slot, dimmed: the canvas shows the two the ticket COULD
// have consulted, not only the one it did.
//
// NOT AN ANIMATED CANVAS. The mockup's numbered edges animate a single
// scenario playing out over ~3 seconds; this draws the finished state of
// one real ask at once -- there is no live "in flight" moment to animate
// for a Detail snapshot already fetched.
class AskBroker extends Component {
  render({ ask, session, index, total }) {
    if (!ask) {
      return html`<div class="empty">no questions were asked during this run</div>`;
    }
    const turns = ask.Turns || [];
    const routed = turns.find((t) => t.Kind === "note");
    const routedTo = routed ? (routed.Text.match(/routed to the (\w+)/) || [])[1] : null;
    const escalate = turns.find((t) => t.Kind === "escalate");
    const finalTurn = turns[turns.length - 1];
    const finalRole = finalTurn && (finalTurn.Kind === "answer" || finalTurn.Kind === "refuse") ? finalTurn.Actor : null;

    const implementerActor = session.Actor || "implementer";
    const implementerColor = actorColorVar(implementerActor);

    return html`
      <div class="askb-crumbbar" style="position:static;margin-bottom:10px">
        <div class="askb-crumb">ask ${index + 1} of ${total}</div>
        <div class="askb-spacer"></div>
        <div class="askb-chip ${ask.Answer || ask.Refused ? "" : "askb-amber"}">
          ${ask.Answer || ask.Refused ? html`<span class="askb-dotlive" style="animation:none;background:var(--ok)"></span>settled` : html`<span class="askb-dotlive"></span>open`}
        </div>
      </div>
      <!-- The node positions below are the mockup's own coordinates
           (docs/design/web/09-ask-broker.html), sized for a dedicated
           full-width page. This canvas lives in the narrower ticket-detail
           column instead, so the OUTER wrapper scrolls horizontally rather
           than rewriting every hardcoded left/top -- the mockup's own zoom
           controls (+/-/expand) already say this canvas is meant to pan,
           not to always fit its container. -->
      <div style="overflow-x:auto;border-radius:9px">
        <div class="askb-canvas" style="height:520px;min-width:1260px;border:1px solid var(--line);border-radius:9px">
        <div class="askb-n askb-ravi" style=${implementerColor ? `border-color:var(${implementerColor})` : ""}>
          <div class="askb-nh">
            <span class="askb-av" style=${implementerColor ? `background:var(${implementerColor});opacity:.2;color:var(${implementerColor})` : ""}
              >${implementerActor[0].toUpperCase()}</span
            >
            <span class="askb-ntitle" style=${implementerColor ? `color:var(${implementerColor})` : ""}>${implementerActor}</span>
          </div>
          <div class="askb-nsub">holds the worktree</div>
          <div class="askb-nfoot">"${ask.Question}"</div>
        </div>

        <div class="askb-n askb-router ${routedTo ? "" : "dim"}" style=${routedTo ? "" : "opacity:.4"}>
          <div class="askb-nh"><span class="askb-av">S</span><span class="askb-ntitle">router</span></div>
          <div class="askb-nsub">haiku · one cheap call · decides whose question this is</div>
          <div class="askb-nfoot">${routedTo ? html`routed to the ${routedTo}` : "not reached"}</div>
        </div>

        <div class="askb-n askb-orion">
          <div class="askb-nh"><span class="askb-av">O</span><span class="askb-ntitle">Orion</span></div>
          <div class="askb-nsub">Go, not a model · spends nothing<br />holds the question and brokers it</div>
          <div class="askb-nfoot">${turns.length} turn${turns.length === 1 ? "" : "s"} recorded</div>
        </div>

        ${ADVISOR_SLOTS.map((slot, i) => {
          const consulted = turns.some((t) => t.Actor === slot.actor);
          const verdictTurn = turns.find((t) => t.Actor === slot.actor && (t.Kind === "answer" || t.Kind === "refuse"));
          const escalatedAway = turns.some((t) => t.Actor === slot.actor && t.Kind === "escalate");
          return html`
            <div key=${i} class="askb-n askb-adv ${slot.cls}" style=${consulted ? "" : "opacity:.35"}>
              <div class="askb-nh">
                <span class="askb-av">${slot.title[0].toUpperCase()}</span>
                <span class="askb-ntitle">${slot.title}</span>
              </div>
              <div class="askb-nsub">sonnet · read-only${consulted ? "" : " · not consulted on this ask"}</div>
              ${verdictTurn
                ? html`<span class="askb-verdict ${verdictTurn.Kind === "refuse" ? "askb-ref" : "askb-ansv"}"
                    >${verdictTurn.Kind === "refuse" ? "refused" : "answered"}</span
                  >`
                : escalatedAway
                ? html`<span class="askb-verdict askb-ref">escalated onward</span>`
                : null}
              ${verdictTurn ? html`<div class="askb-nfoot">${verdictTurn.Text}</div>` : null}
            </div>
          `;
        })}
        </div>
      </div>
    `;
  }
}

// AskBrokerTab is the second tab: one canvas per real ask this run
// recorded, with prev/next -- the mockup's own "ask 1 of 5" affordance,
// applied to however many asks (0 to maxQuestions=5, internal/work's own
// ceiling) this ticket actually made, rather than one fixed sample.
class AskBrokerTab extends Component {
  constructor() {
    super();
    this.state = { index: 0 };
  }

  render({ asks, session }, { index }) {
    if (!asks || asks.length === 0) {
      return html`<div class="empty">no questions were asked during this run</div>`;
    }
    const i = Math.min(index, asks.length - 1);
    return html`
      <div>
        <${AskBroker} ask=${asks[i]} session=${session} index=${i} total=${asks.length} />
        ${asks.length > 1
          ? html`
              <div style="display:flex;gap:8px;margin-top:10px;justify-content:center">
                <button class="pausebtn" disabled=${i === 0} onClick=${() => this.setState({ index: i - 1 })}>← prev ask</button>
                <button class="pausebtn" disabled=${i === asks.length - 1} onClick=${() => this.setState({ index: i + 1 })}>next ask →</button>
              </div>
            `
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
// OrionNode is the orchestrator's own node, ahead of the pipeline. Orion
// never runs a stage -- it only ever hands one off (see Stage.By's own
// comment in detail.go) -- so without this the supervisor that dispatched
// the whole run was invisible in a view that only ever named who a stage
// handed off TO.
class OrionNode extends Component {
  render() {
    return html`
      <div class="sf-node">
        <div class="sf-orb sf-done" style="border-color:rgba(107,113,120,.5);color:var(--ink-dim)">◆</div>
        <div class="sf-nname sf-done">orion</div>
        <div class="sf-nwho">orchestrator</div>
      </div>
      <div class="sf-link sf-done"></div>
    `;
  }
}

// ChildCard is one fan-out participant, in the .sf-fanbox layout
// panel-stageflow.js's own mockup already ships (docs/design/web/07):
// icon, a positional name ("author N" -- the log has no per-child name,
// only a session id nobody should have to read), About (the one field
// that says which case group this session owned), a bar, and its cost.
//
// The bar has no real live-progress signal (About/Cost/Failed are all the
// per-child fields the log carries) -- every child that reported usage
// already finished its share, so the bar is full: green for a clean
// finish, red for one that exited non-zero. This is honestly less
// information than the mockup's own animated in-progress bars draw for a
// STILL-RUNNING fan (which needs a live percentage this static Detail
// snapshot does not have); a finished fan's bars have nothing left to
// animate toward.
class ChildCard extends Component {
  render({ child, i }) {
    const cost = fmtCost(child.Cost);
    return html`
      <div class="sf-child ${child.Failed ? "sf-fail" : "sf-run"}">
        <span class="sf-cico" style="color:${child.Failed ? "var(--fail)" : "var(--ok)"}"
          >${child.Failed ? "✗" : "✓"}</span
        >
        <span class="sf-cname">author ${i + 1}</span>
        <span class="sf-cabout">${child.About || child.Session}</span>
        <span class="sf-cbar"
          ><i class=${child.Failed ? "" : "sf-done"} style="width:100%;${child.Failed ? "background:var(--fail)" : ""}"></i
        ></span>
        ${cost ? html`<span class="sf-ccost">${cost}</span>` : null}
      </div>
    `;
  }
}

// FanBox is one stage's expanded fan-out -- panel-stageflow.js's own
// .sf-fanbox/.sf-fanwrap/.sf-fanrays layout, fed real Detail.Stages[].Children
// instead of that page's hardcoded 5-author sample. The parent node and the
// rays leading to each child are the same visual the mockup draws; what
// changes is that every number and every About string is real.
class FanBox extends Component {
  render({ stage }) {
    const children = stage.Children;
    const totalCost = children.reduce((sum, c) => sum + (c.Cost || 0), 0);
    const failed = children.filter((c) => c.Failed).length;
    return html`
      <div class="sf-sec">
        <div class="sf-seclabel">${stage.Name} · expanded — fan-out</div>
        <div class="sf-fanbox">
          <div class="sf-fanhead">
            <span class="sf-t">${stage.Name} ×${children.length}</span>
            <span class="sf-c"
              >${fmtCost(totalCost)} across ${children.length} author${children.length === 1 ? "" : "s"}${failed > 0
                ? html` · ${failed} failed`
                : null}</span
            >
          </div>
          <div class="sf-fanwrap">
            <div class="sf-fanparent">
              <div class="sf-orb ${stage.Done ? "sf-done" : "sf-now"}">${stage.Done ? "✓" : html`<span class="sf-spin"></span>◐`}</div>
              <div class="sf-nname">${stage.Name}</div>
              <div class="sf-nwho">${stage.Actor}</div>
            </div>
            <div class="sf-fanrays">
              ${children.map((_, i) => html`<div key=${i} class="sf-ray" style="top:${22 + i * 38}px"></div>`)}
            </div>
            <div class="sf-children">
              ${children.map((c, i) => html`<${ChildCard} key=${i} child=${c} i=${i} />`)}
            </div>
          </div>
        </div>
      </div>
    `;
  }
}

// StageNode is clickable: it opens the raw log filtered to this stage
// (Step.Stage, matched by name) in the panel below, per the OR-448 ask
// "when clicked on a stage it should open the log for that". A stage with
// no steps yet (the current stage, before its first tool call lands) is
// still clickable -- the log panel then just shows "no steps yet", not a
// dead click.
class StageNode extends Component {
  render({ stage, last, onSelect, selected }) {
    const cost = fmtCost(stage.Cost);
    const children = stage.Children || [];
    // A fan is a multiplication of cost, said out loud rather than folded
    // into one node's total the way a single-session stage's cost already
    // is -- the same nj-agents §C reasoning internal/work/qafan.go's own
    // comment states for why the count is announced before the spend.
    const fan = children.length > 1;
    return html`
      <div class="sf-node" style="cursor:pointer" onClick=${() => onSelect(stage.Name)}>
        <div class="sf-orb ${stage.Done ? "sf-done" : "sf-now"}" style=${selected ? "box-shadow:0 0 0 2px var(--work)" : ""}
          >${stage.Done ? "✓" : html`<span class="sf-spin"></span>◐`}</div
        >
        <div class="sf-nname ${stage.Done ? "sf-done" : "sf-now"}">${stage.Name}</div>
        <div class="sf-nwho">
          ${stage.Actor}${fan ? html` <span class="sf-badgefan">fan ×${children.length}</span>` : null}${cost
            ? html`<br />${cost}`
            : null}
        </div>
      </div>
      ${!last ? html`<div class="sf-link ${stage.Done ? "sf-done" : ""}"></div>` : null}
    `;
  }
}

// StageLog is the log panel a stage click opens: every Step whose own
// Stage matches, in order -- the same StepRow used by the full raw-log
// drill-down, filtered rather than re-implemented, so the two never
// render a step differently.
class StageLog extends Component {
  render({ stageName, steps, onClose }) {
    const mine = steps.filter((s) => s.Stage === stageName);
    return html`
      <div class="card" style="margin-top:8px">
        <div class="crow">
          <span class="summary" style="font-weight:600">${stageName} · log</span>
          <span class="spacer" style="flex:1"></span>
          <span class="dim" style="cursor:pointer" onClick=${onClose}>✕ close</span>
        </div>
        ${mine.length > 0
          ? html`
              <table style="margin-top:8px">
                <tr><th>Time</th><th>Actor</th><th>Model</th><th>What</th></tr>
                ${mine.map((s, i) => html`<${StepRow} key=${i} step=${s} />`)}
              </table>
            `
          : html`<div class="dim" style="margin-top:8px">no steps recorded yet for this stage</div>`}
      </div>
    `;
  }
}

// StageFlow is the pipeline spine for one run, built from real
// Detail.Stages -- one node per stage crossing this run actually made, in
// the order it made them, prefixed with orion's own node when the first
// crossing names it as the handoff source. Absent (not an empty box) when
// Stages is nil: see the Detail.Stages field comment for when that is (the
// run has not handed off from wherever it started).
//
// Owns its own selectedStage state (a click opens that stage's filtered
// log, per the OR-448 click-to-log ask) -- local rather than lifted to
// DetailPanel, since which stage is expanded is a property of looking at
// the pipeline, not of the ticket as a whole, and resets naturally on
// re-mount (switching tabs, or loading a different ticket) the same way a
// fresh click state should.
class StageFlow extends Component {
  constructor() {
    super();
    this.state = { selectedStage: null };
  }

  render({ stages, cost, steps }, { selectedStage }) {
    if (!stages || stages.length === 0) return null;
    const total = fmtCost(cost);
    const showOrion = stages[0].By === "orion";
    // Every fan (>1 child) gets its own expanded row under the spine, in
    // stage order -- the spine itself stays one node per stage regardless
    // of how many agents ran inside it (panel-stageflow.js's own mockup
    // states the same rule: "a stage is still one stage however many
    // agents it runs").
    const fans = stages.filter((s) => (s.Children || []).length > 1);
    return html`
      <div class="sect">Stage flow${total ? html` <span class="dim">· ${total} so far</span>` : null}</div>
      <div class="sf-flow" style="padding:4px 0 10px">
        ${showOrion ? html`<${OrionNode} />` : null}
        ${stages.map(
          (s, i) => html`<${StageNode}
            key=${i}
            stage=${s}
            last=${i === stages.length - 1}
            selected=${selectedStage === s.Name}
            onSelect=${(name) => this.setState((st) => ({ selectedStage: st.selectedStage === name ? null : name }))}
          />`
        )}
      </div>
      ${fans.map((s, i) => html`<${FanBox} key=${i} stage=${s} />`)}
      ${selectedStage
        ? html`<${StageLog} stageName=${selectedStage} steps=${steps || []} onClose=${() => this.setState({ selectedStage: null })} />`
        : null}
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
    //
    // tab picks between the two real views the mockups drew as separate
    // pages (panel-stageflow.js, panel-askbroker.js) -- merged here as two
    // tabs of the SAME ticket, per the explicit instruction that they
    // belong on one page rather than two destinations.
    this.state = { detail: null, error: null, key: "", run: "", showLog: false, tab: "stageflow" };
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
    this.setState({ key, run, detail: null, error: null, showLog: false, tab: "stageflow" });
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

  render(_, { detail, error, key, showLog, tab }) {
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
          <div class="filter" style="margin-bottom:4px">
            <button class="f ${tab === "stageflow" ? "on" : ""}" onClick=${() => this.setState({ tab: "stageflow" })}>stage flow</button>
            <button class="f ${tab === "askbroker" ? "on" : ""}" onClick=${() => this.setState({ tab: "askbroker" })}>
              ask broker${asks.length > 0 ? html` <span class="dim">· ${asks.length}</span>` : null}
            </button>
          </div>

          ${tab === "stageflow"
            ? html`<${StageFlow} stages=${stages} cost=${detail.Cost} steps=${steps} />`
            : html`<${AskBrokerTab} asks=${asks} session=${detail.Session} />`}

          ${detail.PR
            ? html`
                <div class="sect">Pull request</div>
                <div class="card">
                  <div class="summary"><a href=${detail.PR.URL} target="_blank" rel="noopener">${detail.PR.URL}</a></div>
                  ${detail.PR.CI ? html`<div class="activity">CI: ${detail.PR.CI}</div>` : null}
                </div>
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
