// The stage-flow panel (OR-438): one run's whole pipeline, spine plus
// per-stage detail -- docs/design/web/07-stage-flow.html is the reference
// this is copied from, verbatim.
//
// CHOSEN OVER graph-canvas AND ask-broker (user-confirmed, OR-437/OR-438).
// All three visualize the same kind of run from a different angle; this one
// shows the whole pipeline by default, subsumes the ask/advisor exchange
// (as swim lanes, in the "implementing" section below) and the QA fan-out
// (as its own expandable section) that the other two show in isolation,
// and needs no pan/zoom/minimap layout math to keep looking right once real
// pipeline data varies in length -- the risk graph-canvas carries and this
// page does not.
//
// STATIC AND UNWIRED, ON PURPOSE (phase 1 of OR-438). Nothing in this
// codebase yet emits per-stage timing/actor/cost, which stages were
// skipped and why, the fan-out shape, or the ask/advisor exchange shape --
// so this renders the mockup's own baked-in sample exactly as drawn.
// Wiring it to a real run is phase 2, once that data model and API exist.
//
// EVERY CLASS PREFIXED sf-. Same reason as panel-askbroker.js: index.html's
// shared stylesheet already defines .dotlive and .chip with different
// colours/animations than this mockup's own versions of those names.
//
// Class components only, no hooks -- see panel-run.js for why.
import { Component, h } from "/vendor/preact.module.js";
import htm from "/vendor/htm.module.js";
import { registerPanel } from "./panels.js";

const html = htm.bind(h);

class StageFlowPanel extends Component {
  render() {
    return html`
      <div class="sf-main">
        <div class="sf-topbar">
          <div class="sf-crumb">run / OR-272</div>
          <div class="sf-spacer"></div>
          <div class="sf-chip"><span class="sf-dotlive"></span>live</div>
          <div class="sf-chip">× close</div>
        </div>

        <div class="sf-hero">
          <span class="sf-key">OR-272</span>
          <span class="sf-htitle">The gate board: everything waiting on me, across projects</span>
          <span class="sf-spacer"></span>
          <span class="sf-state">◐ working · qa</span>
        </div>

        <div class="sf-sec">
          <div class="sf-seclabel">Pipeline</div>
          <div class="sf-secnote">
            The spine is the stage chain. A stage that fans out or holds an exchange carries a badge and expands
            below — the spine stays one line per stage, because a stage is still one stage however many agents it
            runs.
          </div>
          <div class="sf-flow">
            <div class="sf-node">
              <div class="sf-orb sf-done">✓</div>
              <div class="sf-nname sf-done">routing</div>
              <div class="sf-nwho">Sam · dispatcher</div>
            </div>
            <div class="sf-link sf-done"></div>
            <div class="sf-node">
              <div class="sf-orb sf-done">✓</div>
              <div class="sf-nname sf-done">implementing</div>
              <div class="sf-nwho">Ravi · opus<br />$2.06</div>
              <span class="sf-badgefan" style="background:rgba(214,162,49,.14);color:var(--warn);border-color:rgba(214,162,49,.3)"
                >2 asks</span
              >
            </div>
            <div class="sf-link sf-done"></div>
            <div class="sf-node">
              <div class="sf-orb sf-skip">−</div>
              <div class="sf-nname sf-todo">dba</div>
              <div class="sf-nwho">skipped — no<br />schema change</div>
            </div>
            <div class="sf-link sf-live"></div>
            <div class="sf-node">
              <div class="sf-orb sf-now"><span class="sf-spin"></span>◐</div>
              <div class="sf-nname sf-now">qa</div>
              <div class="sf-nwho">Anita · sonnet<br />$0.41</div>
              <span class="sf-badgefan">fan ×5</span>
            </div>
            <div class="sf-link"></div>
            <div class="sf-node">
              <div class="sf-orb sf-todo">○</div>
              <div class="sf-nname sf-todo">push</div>
              <div class="sf-nwho">orion</div>
            </div>
            <div class="sf-link"></div>
            <div class="sf-node">
              <div class="sf-orb sf-todo">○</div>
              <div class="sf-nname sf-todo">pull request</div>
              <div class="sf-nwho">Dana · PR writer</div>
            </div>
            <div class="sf-link"></div>
            <div class="sf-node">
              <div class="sf-orb sf-todo">⌛</div>
              <div class="sf-nname sf-todo">ci</div>
              <div class="sf-nwho">no agent runs<br />nothing spent</div>
            </div>
            <div class="sf-link"></div>
            <div class="sf-node">
              <div class="sf-orb sf-todo">○</div>
              <div class="sf-nname sf-todo">approval</div>
              <div class="sf-nwho">you</div>
            </div>
            <div class="sf-link"></div>
            <div class="sf-node">
              <div class="sf-orb sf-todo">○</div>
              <div class="sf-nname sf-todo">promotion</div>
              <div class="sf-nwho">a person, never<br />the watcher</div>
            </div>
          </div>
        </div>

        <div class="sf-sec">
          <div class="sf-seclabel">qa · expanded — fan-out</div>
          <div class="sf-secnote">
            One stage, five concurrent authors. The count is said out loud <b>before</b> the spend, not discovered
            in the bill — so the panel leads with it too.
          </div>
          <div class="sf-fanbox">
            <div class="sf-fanhead">
              <span class="sf-t">authoring ×5</span>
              <span class="sf-c">18 cases across 5 authors · all sonnet · capped at max_concurrent_children</span>
            </div>
            <div class="sf-fansaid">
              Anita · QA engineer said: <b>writing 18 case(s) across 5 authors</b> — a fan is a
              multiplication of cost, and a reader who sees the agent count only in the bill has been told too
              late.
            </div>

            <div class="sf-fanwrap">
              <div class="sf-fanparent">
                <div class="sf-orb sf-now"><span class="sf-spin"></span>◐</div>
                <div class="sf-nname sf-now">qa</div>
                <div class="sf-nwho">Anita · sonnet</div>
              </div>
              <div class="sf-fanrays">
                <div class="sf-ray" style="top:22px"></div>
                <div class="sf-ray" style="top:60px;animation-delay:.4s"></div>
                <div class="sf-ray" style="top:98px;animation-delay:.8s"></div>
                <div class="sf-ray" style="top:136px;animation-delay:1.2s"></div>
                <div class="sf-ray" style="top:174px;animation-delay:1.6s"></div>
              </div>
              <div class="sf-children">
                <div class="sf-child">
                  <span class="sf-cico" style="color:var(--ok)">✓</span>
                  <span class="sf-cname">author 1</span>
                  <span class="sf-cabout">4 case(s) · column derivation</span>
                  <span class="sf-cbar"><i class="sf-done" style="width:100%"></i></span>
                  <span class="sf-ccost">$0.11</span>
                </div>
                <div class="sf-child sf-run">
                  <span class="sf-cico" style="color:var(--work)">◐</span>
                  <span class="sf-cname">author 2</span>
                  <span class="sf-cabout">4 case(s) · waiting-on-me default view</span>
                  <span class="sf-cbar"><i style="width:62%"></i></span>
                  <span class="sf-ccost">$0.07</span>
                </div>
                <div class="sf-child sf-run">
                  <span class="sf-cico" style="color:var(--work)">◐</span>
                  <span class="sf-cname">author 3</span>
                  <span class="sf-cabout">4 case(s) · output escaping</span>
                  <span class="sf-cbar"><i style="width:38%"></i></span>
                  <span class="sf-ccost">$0.05</span>
                </div>
                <div class="sf-child sf-fail">
                  <span class="sf-cico" style="color:var(--fail)">✗</span>
                  <span class="sf-cname">author 4</span>
                  <span class="sf-cabout">3 case(s) · path validation — exited non-zero</span>
                  <span class="sf-cbar"><i style="width:100%;background:var(--fail)"></i></span>
                  <span class="sf-ccost">$0.04</span>
                </div>
                <div class="sf-child sf-run">
                  <span class="sf-cico" style="color:var(--work)">◐</span>
                  <span class="sf-cname">author 5</span>
                  <span class="sf-cabout">3 case(s) · credential isolation</span>
                  <span class="sf-cbar"><i style="width:24%"></i></span>
                  <span class="sf-ccost">$0.03</span>
                </div>
              </div>
            </div>

            <div class="sf-fanfoot">
              <b>A failed author is reported and otherwise ignored.</b> Its cases stay in the list handed to the QA
              session, which reads the diff and writes what it finds missing — so author 4 costs a retry of that
              group's work, never the cases themselves. The stage does not fail because a child did.
            </div>
          </div>
        </div>

        <div class="sf-sec" style="padding-bottom:28px">
          <div class="sf-seclabel">implementing · expanded — the exchange</div>
          <div class="sf-secnote">
            When an implementer stops on a question it does not go to you. A router picks an advisor, the advisor
            answers or refuses, and the run resumes — all inside one stage. Read top to bottom; each arrow is one
            turn.
          </div>
          <div class="sf-exbox">
            <div class="sf-exhead">
              <span class="sf-t">2 asks during implementation</span>
              <span class="sf-c">$0.19 across 4 advisor turns · the implementer stays resumed throughout</span>
            </div>

            <div class="sf-lanes">
              <div class="sf-lane">
                <div class="sf-orb sf-done" style="border-color:rgba(220,182,122,.5);color:#dcb67a;background:rgba(220,182,122,.08)">
                  R
                </div>
                <div class="sf-lanehead">Ravi</div>
                <div class="sf-lanesub">backend developer<br />opus</div>
                <div class="sf-laneline"></div>
              </div>

              <div class="sf-turns">
                <div class="sf-turn">
                  <span class="sf-turnlab">stops: "which store owns the gate list?"</span>
                  <span class="sf-arrow sf-r"></span>
                </div>
                <div class="sf-turn">
                  <span class="sf-turnlab">Sam · haiku routes it → architect</span>
                  <span class="sf-arrow sf-r" style="background:rgba(107,113,120,.3)"></span>
                </div>
                <div class="sf-turn">
                  <span class="sf-turnlab sf-refuse">architect refuses — not grounded in the repo</span>
                  <span class="sf-arrow sf-l"></span>
                </div>
                <div class="sf-turn">
                  <span class="sf-turnlab">re-asked: product manager</span>
                  <span class="sf-arrow sf-r"></span>
                </div>
                <div class="sf-turn">
                  <span class="sf-turnlab">answer + grounding → run resumes</span>
                  <span class="sf-arrow sf-l" style="background:rgba(75,181,67,.4)"></span>
                </div>
              </div>

              <div class="sf-lane">
                <div class="sf-orb sf-done" style="border-color:rgba(181,140,240,.5);color:var(--t-violet);background:rgba(181,140,240,.08)">
                  A
                </div>
                <div class="sf-lanehead">advisors</div>
                <div class="sf-lanesub">architect → PM<br />sonnet · read-only</div>
                <div class="sf-laneline"></div>
              </div>
            </div>

            <div class="sf-exfoot">
              <b>A refusal is not a failure and never escalates the model.</b> Refusal means the artifact is silent
              — a stronger model is not more likely to find something that is not there, only more likely to
              produce a confident answer anyway. So the question moves sideways to another advisor, still on
              sonnet. Only when both refuse does it reach you, and the board shows it as a question gate.
            </div>
          </div>
        </div>
      </div>
    `;
  }
}

registerPanel("stageflow", "stage flow", StageFlowPanel, false);
