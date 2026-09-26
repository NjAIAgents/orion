// The ask-broker panel (OR-437): one blocked ask, routed through a
// dispatcher to an advisor and back -- docs/design/web/09-ask-broker.html
// is the reference this is copied from, verbatim.
//
// STATIC AND UNWIRED, ON PURPOSE. Nothing in this codebase yet emits the
// data this page would need -- which advisor was asked, what they answered,
// where each node sits on the canvas -- so this renders the mockup's own
// baked-in sample exactly as drawn. Wiring it to a real ask is separate,
// future scope once that event/API design exists.
//
// NOT THE RUN-DETAIL PAGE. That is panel-detail.js
// (docs/design/web/03-ticket-detail.html). This is a distinct visualization
// of one specific moment -- an implementer blocked mid-run on an advisor
// question -- reached from nowhere in the nav yet, the same inNav:false
// shape as detail.
//
// EVERY CLASS PREFIXED askb-. index.html's shared stylesheet already
// defines .dotlive and .chip with different colours and meanings than this
// mockup's own versions of those names; prefixing avoids a collision rather
// than reconciling two designs that were never meant to agree.
//
// Class components only, no hooks -- see panel-run.js for why.
import { Component, h } from "/vendor/preact.module.js";
import htm from "/vendor/htm.module.js";
import { registerPanel } from "./panels.js";

const html = htm.bind(h);

class AskBrokerPanel extends Component {
  render() {
    return html`
      <div class="askb-main">
        <div class="askb-crumbbar">
          <div class="askb-crumb">run / OR-272 / implementing / ask 1</div>
          <div class="askb-spacer"></div>
          <div class="askb-chip askb-amber"><span class="askb-dotlive"></span>ask in flight</div>
          <div class="askb-chip">ask 1 of 5</div>
        </div>

        <div class="askb-canvas">
          <svg class="askb-edges">
            <path class="askb-e askb-ask" d="M 247 392 C 310 392, 330 380, 392 378" />
            <circle r="3" fill="var(--amber)">
              <animateMotion dur="3s" repeatCount="indefinite" path="M 247 392 C 310 392, 330 380, 392 378" />
            </circle>
            <text class="askb-num" x="286" y="370" fill="var(--amber)">1</text>
            <text class="askb-elab askb-amber" x="258" y="424">"which store owns</text>
            <text class="askb-elab askb-amber" x="258" y="436">the gate list?"</text>

            <path class="askb-e askb-route" d="M 470 318 C 470 288, 470 266, 470 232" />
            <path class="askb-e askb-route" d="M 530 232 C 530 266, 530 288, 530 318" />
            <circle r="2.6" fill="var(--ink-faint)">
              <animateMotion dur="3s" repeatCount="indefinite" path="M 470 318 C 470 288, 470 266, 470 232" />
            </circle>
            <text class="askb-num" x="444" y="280" fill="var(--ink-faint)">2</text>
            <text class="askb-elab" x="548" y="282">→ architect</text>

            <path class="askb-e askb-inv" d="M 615 350 C 700 350, 720 254, 800 250" />
            <circle r="3" fill="var(--t-violet)">
              <animateMotion dur="3s" begin="0.4s" repeatCount="indefinite" path="M 615 350 C 700 350, 720 254, 800 250" />
            </circle>
            <text class="askb-num" x="690" y="296" fill="var(--t-violet)">3</text>
            <text class="askb-elab askb-violet" x="640" y="326">invoke · with artifacts</text>

            <path class="askb-e askb-refuse" d="M 800 292 C 716 300, 700 372, 615 376" />
            <circle r="3" fill="var(--warn)">
              <animateMotion dur="3s" begin="1.2s" repeatCount="indefinite" path="M 800 292 C 716 300, 700 372, 615 376" />
            </circle>
            <text class="askb-num" x="706" y="352" fill="var(--warn)">4</text>
            <text class="askb-elab askb-warn" x="636" y="398">refused · artifacts silent</text>

            <path class="askb-e askb-inv" d="M 615 404 C 700 410, 716 482, 800 486" />
            <circle r="3" fill="var(--t-violet)">
              <animateMotion dur="3s" begin="1.8s" repeatCount="indefinite" path="M 615 404 C 700 410, 716 482, 800 486" />
            </circle>
            <text class="askb-num" x="690" y="456" fill="var(--t-violet)">5</text>
            <text class="askb-elab askb-violet" x="628" y="440">forwarded once</text>

            <path class="askb-e askb-ans" d="M 800 528 C 700 534, 660 440, 615 424" />
            <circle r="3" fill="var(--ok)">
              <animateMotion dur="3s" begin="2.2s" repeatCount="indefinite" path="M 800 528 C 700 534, 660 440, 615 424" />
            </circle>
            <text class="askb-num" x="690" y="512" fill="var(--ok)">6</text>
            <text class="askb-elab askb-ok" x="634" y="540">answer + grounding</text>

            <path class="askb-e askb-ans" d="M 392 410 C 330 416, 310 420, 247 420" />
            <circle r="3" fill="var(--ok)">
              <animateMotion dur="3s" begin="2.6s" repeatCount="indefinite" path="M 392 410 C 330 416, 310 420, 247 420" />
            </circle>
            <text class="askb-num" x="312" y="446" fill="var(--ok)">7</text>
            <text class="askb-elab askb-ok" x="258" y="462">resumes · same session</text>
          </svg>

          <div class="askb-n askb-ravi">
            <span class="askb-port" style="right:-4px;top:48px;border-color:rgba(220,182,122,.5)"></span>
            <span class="askb-port" style="right:-4px;top:76px;border-color:rgba(75,181,67,.5)"></span>
            <div class="askb-nh"><span class="askb-av">R</span><span class="askb-ntitle">Ravi</span></div>
            <div class="askb-nsub">backend developer · opus<br />holds the worktree</div>
            <div style="margin-top:9px">
              <span class="askb-idlebadge"><span class="askb-zz">●</span> idle 14s · $0.00</span>
            </div>
            <div class="askb-nfoot">blocked on ask 1<span class="askb-r">$2.06 so far</span></div>
          </div>

          <div class="askb-n askb-router">
            <span class="askb-port" style="left:74px;bottom:-4px"></span>
            <span class="askb-port" style="left:134px;bottom:-4px"></span>
            <div class="askb-nh">
              <span class="askb-av">S</span><span class="askb-ntitle" style="color:#c9a0dc">Sam · dispatcher</span>
            </div>
            <div class="askb-nsub">haiku · one cheap call<br />decides whose question this is</div>
            <div class="askb-nfoot">routed to the architect<span class="askb-r">$0.01</span></div>
          </div>

          <div class="askb-n askb-orion">
            <span class="askb-hubpulse"></span>
            <span class="askb-port" style="left:-4px;top:60px;border-color:rgba(220,182,122,.5)"></span>
            <span class="askb-port" style="left:-4px;top:92px;border-color:rgba(75,181,67,.5)"></span>
            <span class="askb-port" style="right:-4px;top:32px;border-color:rgba(181,140,240,.5)"></span>
            <span class="askb-port" style="right:-4px;top:58px;border-color:rgba(214,162,49,.5)"></span>
            <span class="askb-port" style="right:-4px;top:86px;border-color:rgba(181,140,240,.5)"></span>
            <span class="askb-port" style="right:-4px;top:106px;border-color:rgba(75,181,67,.5)"></span>
            <div class="askb-nh"><span class="askb-av">O</span><span class="askb-ntitle">Orion</span></div>
            <div class="askb-nsub">Go, not a model · spends nothing<br />holds the question and brokers it</div>
            <div class="askb-steps">
              <span class="askb-st askb-done"></span><span class="askb-st askb-done"></span>
              <span class="askb-st askb-warnb"></span><span class="askb-st askb-done"></span>
              <span class="askb-st askb-on"></span>
            </div>
            <div class="askb-nfoot">
              took · routed · refused · forwarded · returning<span class="askb-r">14s</span>
            </div>
          </div>

          <div class="askb-n askb-adv askb-arch">
            <span class="askb-port" style="left:-4px;top:54px;border-color:rgba(181,140,240,.5)"></span>
            <span class="askb-port" style="left:-4px;top:96px;border-color:rgba(214,162,49,.5)"></span>
            <div class="askb-nh"><span class="askb-av">N</span><span class="askb-ntitle">Navjyot · architect</span></div>
            <div class="askb-nsub">sonnet · read-only · given the ticket's artifacts</div>
            <span class="askb-verdict askb-ref">refused — the artifacts are silent</span>
            <div class="askb-nfoot">names the doc to amend<span class="askb-r">$0.06</span></div>
          </div>

          <div class="askb-n askb-adv askb-pm">
            <span class="askb-spinring"></span>
            <span class="askb-port" style="left:-4px;top:54px;border-color:rgba(181,140,240,.5)"></span>
            <span class="askb-port" style="left:-4px;top:96px;border-color:rgba(75,181,67,.5)"></span>
            <div class="askb-nh"><span class="askb-av">P</span><span class="askb-ntitle">Priya · product manager</span></div>
            <div class="askb-nsub">sonnet · read-only · the one forward</div>
            <span class="askb-verdict askb-ansv">answering — with grounding</span>
            <div class="askb-nfoot">the run resumes on this<span class="askb-r">$0.12</span></div>
          </div>

          <div class="askb-legend">
            <div class="askb-lg"><span class="askb-lgl" style="border-color:rgba(220,182,122,.6)"></span>question</div>
            <div class="askb-lg">
              <span class="askb-lgl" style="border-color:rgba(107,113,120,.5);border-top-style:dashed"></span>routing · haiku
            </div>
            <div class="askb-lg"><span class="askb-lgl" style="border-color:rgba(181,140,240,.6)"></span>invoke advisor</div>
            <div class="askb-lg">
              <span class="askb-lgl" style="border-color:rgba(214,162,49,.6);border-top-style:dashed"></span>refusal
            </div>
            <div class="askb-lg"><span class="askb-lgl" style="border-color:rgba(75,181,67,.6)"></span>answer</div>
          </div>

          <div class="askb-callout" style="left:46px;top:520px">
            <b>Ravi never meets an advisor.</b> He asked one question and will get one answer — he does not learn
            that two were consulted. That is what keeps the advisor's reading out of his context.
          </div>
          <div class="askb-callout askb-w" style="left:1042px;top:200px;max-width:210px">
            <b>A refusal never escalates the model.</b> Silence in the artifacts is not something a stronger model
            finds — only something it answers more confidently. It moves sideways, still sonnet.
          </div>
          <div class="askb-callout" style="left:1042px;top:436px;max-width:210px">
            <b>One forward, then a person.</b> Not a broadcast — asking every advisor in turn would pay the full
            price of the ambiguity every time the artifacts are silent.
          </div>

          <div class="askb-controls">
            <div class="askb-ctl">+</div><div class="askb-ctl">−</div><div class="askb-ctl">⤢</div>
          </div>
          <div class="askb-hint">ask 1 of 5 allowed · the sixth stops the run and reaches your gate board</div>
        </div>
      </div>
    `;
  }
}

registerPanel("askbroker", "ask broker", AskBrokerPanel, false);
