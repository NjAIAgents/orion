# 0024: The local web surface is authenticated by a per-process token; Origin and Host checks are additive layers

- Status: Accepted (by Navjyot, 2026-09-09)
- Date: 2026-09-09
- Tickets: OR-267 (this decision), OR-266 (the story it gates), OR-268 /
  OR-269 / OR-270 (the three layers it specifies), OR-35 (the epic whose
  "no auth" line it overturns)
- Related: [0001](0001-precedence-rule-orion-owns-orchestration.md) (Orion owns
  orchestration; a gate that a stage can talk its way past is not a gate)

## Context

OR-35 specified the local web surface as "Localhost only, no auth". That was
sound while the surface was read-only: the worst a hostile local process could
do was read what the operator could already read on their own terminal.

The surface is no longer read-only. v0.10.0 and v0.11.0 put three write paths
behind it — approving a human gate (OR-281), editing agent config (OR-54,
mocked in `docs/design/web/06-config-panel.html`), and starting a run by
shelling out to the CLI. Each of those is an action taken *as the operator*,
with the operator's credentials, on the operator's repositories.

A no-auth page bound to 127.0.0.1 is reachable by:

1. **Any other process on the machine.** Loopback is not a permission
   boundary. Any user process — a dependency's postinstall script, a browser
   extension's native host, anything the operator ran once — can `POST` to it.
2. **Any website the operator visits**, via CSRF. An HTML form on `evil.com`
   can POST cross-origin to `http://127.0.0.1:PORT/…` with no read of the
   response required — approving a gate needs no response.
3. **Any website the operator visits**, via DNS rebinding. `evil.com` resolves
   to the attacker's address on first load and to `127.0.0.1` a moment later.
   The page is then same-origin with the surface by the browser's own rules,
   so it can read responses as well as write.

A pre-implementation security review (confidence 92) rejected the framing that
made this a choice between origin checking, a token, and an argued acceptance:
**origin checking cannot satisfy threat 1 at all.** `Origin` and
`Sec-Fetch-Site` are set by the browser and withheld from JavaScript precisely
so a page cannot forge them. A local process is not a browser. It sets whatever
headers it likes, including none. A control whose entire enforcement lives in
the attacker's own client is worth zero against that attacker.

So the three controls are not comparable options. They cover different threats,
and only one of them covers the first.

## Decision

**Three layers, all mandatory, applied as one middleware over the whole mux.**
Not three alternatives, and not a per-handler check.

### Layer 1 — a per-process token (OR-268). Mandatory.

- Minted at server start from `crypto/rand`, **at least 128 bits**, hex-encoded.
  Per process: it does not persist, is not derived from anything on disk, and a
  restart invalidates every page still holding the old one.
- Compared with `crypto/subtle.ConstantTimeCompare`. The comparison is against
  a value an attacker can guess at over a local socket with no rate limit, so
  the timing side channel is not theoretical the way it is on a WAN.
- **Delivered to the page, and sent back, in a custom request header only** —
  `X-Orion-Token`. Never a cookie, never a query string.

The transport is not a style preference; both of the convenient alternatives
re-open the hole the token exists to close:

- **A cookie is ambient authority.** The browser attaches it to a cross-site
  POST *for* the attacker. A cookie-borne token authenticates the browser, not
  the page, so it authenticates `evil.com`'s form submission exactly as
  readily as the surface's own fetch. `SameSite=Strict` narrows this but is a
  second thing to get right, is not enforced by the server, and leaves the
  token in a store that outlives the process that minted it.
- **A query string leaks by design.** It lands in browser history, in the
  `Referer` of any outbound link, in shell history when the operator pastes a
  `curl`, and in any log that records a request line.

A custom header, by contrast, **cannot be set by a cross-origin HTML form** —
form submissions have no header control, and any header-setting request from
JavaScript is subject to CORS preflight, which the surface does not answer. So
this single choice is both the authentication transport and the CSRF defence.

### Layer 2 — Origin and Sec-Fetch-Site, default-deny (OR-269). Additive.

Every state-changing request must carry an `Origin` that exactly matches a
loopback origin for the bound port, and a `Sec-Fetch-Site` of `same-origin`.
**A missing header is a rejection, not a pass.**

The empty case is the whole point. The intuitive shape —

```go
if origin != "" && !isLoopback(origin) { reject }   // WRONG
```

— fails open exactly where it matters: a cross-origin HTML form POST is one of
the few requests a browser sends *without* an `Origin`, and non-browser clients
send nothing at all. A check that admits the empty header admits both attackers
it was added to stop while reading, at a glance, as though it stops them.

Requiring `Sec-Fetch-Site: same-origin` sets a browser floor (Chrome 76,
Firefox 90, Safari 16.4). That is accepted: the surface is a developer tool
launched from the operator's own terminal, and a browser that predates 2023 is
not a supported client for a page that approves merges.

### Layer 3 — an exact-string Host allowlist (OR-270). Additive.

`Host` is split into host and port, and the host part must match one of
`127.0.0.1`, `::1`, `localhost` exactly, with the port equal to the bound port.
Applied to **every** request, not only writes: a rebound page reading a run's
logs is a leak even if it can write nothing.

Both intuitive implementations are bypassable and must not appear:

- **A substring or suffix test** on `localhost` or `127.0.0.1` matches
  `localhost.evil.com` and `127.0.0.1.evil.com`, both of which an attacker can
  register and point at loopback.
- **Resolving the `Host` value** is worse. It performs a DNS lookup on
  attacker-controlled input and then trusts the answer — and the attacker
  simply publishes an `A` record for `127.0.0.1`, which is not a bypass of DNS
  rebinding but a description of it.

### What each layer stops, and what it does not

| Threat | Token | Origin / Sec-Fetch-Site | Host allowlist |
|---|---|---|---|
| Another local process POSTs to a write endpoint | **stops** | no — it forges or omits the header | no — it sends a correct `Host` |
| Cross-origin form POST from a visited website (CSRF) | **stops** — a form cannot set a header | **stops** | no |
| DNS rebinding: attacker page becomes same-origin | no — but it still cannot read the token | no — the browser reports `same-origin` truthfully | **stops** |
| Operator pastes the token into a hostile page | no | no | no |

Each column has a row it cannot cover. That is why there are three, and why
"pick the strongest one" is not available.

### "Argued acceptance" is not an outcome

OR-266's acceptance criteria are absolute. This ADR decides *how* the surface
is authenticated, not *whether*. A later change that removes a layer because it
is inconvenient is reopening a decision, not making a small one.

## Consequences

- **Nothing that mutates state ships before the middleware does.** The
  enforcement point lands first, in `internal/localauth`, so the tickets that
  add write endpoints inherit it rather than each remembering to call it.
- **Default-deny means new routes are protected by accident, not by
  diligence.** The middleware wraps the whole mux and exempts an explicit
  allowlist of read-only paths, `GET`/`HEAD` only. A handler added later — a
  write endpoint, or a `GET` that mutates — is guarded until someone
  deliberately writes it into the allowlist, and a test asserts that an
  unlisted route rejects an unauthenticated POST.
- **The token has to reach the page, and that path is the remaining risk.**
  The server that serves the page prints the token once to the launching
  terminal, or writes it `0600` under the user's runtime directory, and inlines
  it into the page it serves. It must never be echoed back by an API response:
  a credential rendered into a page is a credential that has left the store —
  the same rule `docs/design/web/06-config-panel.html` applies to the
  credentials the config panel edits.
- **A restart invalidates open tabs.** Per-process is deliberate. Reloading the
  page is the recovery, and it costs less than a token whose lifetime nobody
  can reason about.
- **Authentication is defence in depth over argv validation, not a substitute
  for it.** The write path that starts a run shells out to the CLI. Argument
  discipline is specified on the stories that shell out; an authenticated
  caller is still a caller.
- **This overturns OR-35's "Localhost only, no auth."** That line is updated to
  name this mechanism and link here (OR-271), so the epic no longer states a
  constraint the product does not hold to.
