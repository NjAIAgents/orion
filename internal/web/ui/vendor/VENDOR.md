# Vendored UI runtime

Two committed files, 12,636 bytes total. **The committed file IS the pin; this note is
what makes the pin checkable** (OR-66).

There is no build step. These are the files the browser runs — no bundler, no
`node_modules`, no `make ui` target. A machine with no Node installed must still
produce a working binary from `go build ./...`.

| File | Package | Version | Bytes | SHA-256 |
|---|---|---|---|---|
| `preact.module.js` | preact | 10.24.3 | 11,429 | `9ba8bb591b0c9a72132ff65a76e69c5a4e49cf71721b47b659f3243658bde05b` |
| `htm.module.js` | htm | 3.1.1 | 1,207 | `ab33dd3f38059b9be4d5f5350128eefb2356639c4e0bbe9d9e8b3ba75847e9e4` |

## Sources

Fetched 2026-09-09 from cdnjs, which serves the packages' own published ESM builds:

- https://cdnjs.cloudflare.com/ajax/libs/preact/10.24.3/preact.module.js
- https://cdnjs.cloudflare.com/ajax/libs/htm/3.1.1/htm.module.js

## Verifying

```sh
cd internal/web/ui/vendor
shasum -a 256 -c <<'EOF'
9ba8bb591b0c9a72132ff65a76e69c5a4e49cf71721b47b659f3243658bde05b  preact.module.js
ab33dd3f38059b9be4d5f5350128eefb2356639c4e0bbe9d9e8b3ba75847e9e4  htm.module.js
EOF
```

## What is here, and what is deliberately not

`preact.module.js` is self-contained — it imports nothing. `htm.module.js` is
`export default` and also imports nothing, so the two files have no module-resolution
requirements at all:

```js
import { render, Component } from './vendor/preact.module.js';
import htm from './vendor/htm.module.js';
const html = htm.bind(Component.prototype.constructor);
```

**`hooks.module.js` is NOT vendored, on purpose.** Preact's hooks build begins with
`import { options } from "preact"` — a *bare* specifier the browser cannot resolve
without an import map or a bundler, which is precisely what OR-66 rules out. Adding it
would also make three files where the story specifies two. Class components cover what
the mockups in `docs/design/web/` need.

If hooks become genuinely necessary later, the honest options are an import map in the
served HTML or a rewritten specifier — both are a decision to take deliberately, with
this note updated to record it, rather than a file quietly added.

## The one rule that keeps this safe (OR-289)

**`dangerouslySetInnerHTML` is banned in `internal/web/ui/**`.**

The runtime escapes by construction, not by care. `htm.module.js` contains no
DOM-writing code at all — it parses a tagged template into vdom, and an interpolated
value becomes a vdom *child*, never markup. In `preact.module.js` every `innerHTML`
write sits inside the `dangerouslySetInnerHTML` branch (keyed on `__html`); text
children go through `createTextNode` and `.data=`, which cannot execute markup.

So `${summary}` holding `<img src=x onerror=…>` renders as literal text — and that
matters here, because ticket summaries and event payloads come from a tracker other
people can write to, and this page is the control plane once the write endpoints exist.

That leaves exactly one way to reintroduce the risk: reaching for the escape hatch.
Nothing this UI draws needs it. Server-rendered paths are covered separately by
OR-276 (`html/template`, no `template.HTML` on scanned values) and OR-285 (stderr
scrubbed, capped, rendered as text).

## Upgrading

Fetch the new version, recompute both checksums, and update the table in the same
commit as the files. A pin whose checksum no longer matches its file is worse than no
pin: it reads as verified while being false.
