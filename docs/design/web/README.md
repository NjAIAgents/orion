# Orion web — design mockups (v0.10.0)

Static mockups for the `orion web` surface. **Reference material, not shippable code** —
no build step, no dependencies; open any file directly in a browser.

Live, editable versions in Claude Design:
<https://claude.ai/design/p/866a952e-3d50-4833-8b57-b53f93655b86>

## Pages

| File | Shows | Tickets |
|---|---|---|
| `00-brief.html` | Assumptions, palette, open questions — **read first** | — |
| `01-run-view.html` | Card grid + streamed log panel | OR-51, OR-67, OR-68, OR-69 |
| `02-gate-board.html` | Waiting-on-me across projects, with gate controls | OR-272 – OR-278, OR-281 |
| `03-ticket-detail.html` | One ticket end to end, cost per actor | OR-53, OR-76, OR-79 |
| `04-agents.html` | Roster: model per role and why | OR-54, OR-80, OR-81 |
| `05-launcher.html` | Start work — `orion work` / `queue add` / `watch` | unticketed |
| `06-config-panel.html` | Config slide-over; dangerous fields terminal-only | unticketed |
| `07-stage-flow.html` | Linear pipeline + fan/exchange expansions | superseded by 08 |
| `08-graph-canvas.html` | **Graph canvas** — nodes, bezier edges, minimap | OR-51, OR-53 |
| `09-ask-broker.html` | **Ask exchange** — Orion brokers between agents | OR-51 |

`08` and `09` are the chosen direction. `07` is kept as the rejected alternative.

## Where the visual language comes from

Lifted from `internal/ui`, not invented — so the browser and the console never disagree:

- **Five verbs, no others**: ok, working, waiting, warning, failed (`ui/event.go`)
- **Colour is never the sole carrier** — every state has a word beside it (`ui/color.go`)
- **Three colour axes**: ticket by key, status by outcome, actor by actor
- **"Name · job title" on every line** (`internal/actors`)
- **Stage boundaries drop the columns** for one sentence (`ui/stage.go`)
- **Cost is a floor when usage is missing** — "the ticket cost at least this much" (`cost/render.go`)

Sample data uses the shipped roster; names are operator-configurable, so treat them as examples.

## Status

Mockups only. Nothing here is wired to a server, and `orion web` does not exist yet.
Once OR-50/OR-51 land, `/claude-design-pull` can hold the built page to these as a parity gate.
