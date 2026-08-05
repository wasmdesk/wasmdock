<p align="center">
  <img src="https://raw.githubusercontent.com/wasmdesk/brand/main/png/color/256/wasmdesk.png" alt="wasmdesk" width="88" height="88">
</p>

<h1 align="center">wasmdock</h1>
<p align="center"><strong>The dock / toolbar for the wasmdesk WebAssembly desktop.</strong></p>

<p align="center">
  A bottom-anchored, Fluxbox-style toolbar — launchers, a live window list, a
  workspace switcher and a clock — that runs as a standalone
  <a href="https://github.com/wasmdesk/wasmbox">wasmbox</a> external client,
  written in pure Go (CGO=0) and rendered into a shared surface.
</p>

<p align="center">
  <a href="https://github.com/wasmdesk"><img alt="part of wasmdesk" src="https://img.shields.io/badge/wasmdesk-the%20WASM%20desktop-1a7f37?style=flat-square"></a>
  <a href="https://github.com/wasmdesk/wasmbox"><img alt="client of wasmbox" src="https://img.shields.io/badge/client%20of-wasmbox-9B1C2E?style=flat-square"></a>
  <a href="https://github.com/go-widgets/toolkit"><img alt="built on go-widgets/toolkit" src="https://img.shields.io/badge/widgets-go--widgets%2Ftoolkit-8B5CF6?style=flat-square"></a>
  <img alt="WebAssembly" src="https://img.shields.io/badge/WebAssembly-CGO%3D0-654FF0?style=flat-square&logo=webassembly&logoColor=white">
  <a href="LICENSE"><img alt="License: BSD-3-Clause" src="https://img.shields.io/badge/license-BSD--3--Clause-blue?style=flat-square"></a>
</p>

---

`wasmdock` is the **dock / toolbar client** for
[`wasmbox`](https://github.com/wasmdesk/wasmbox), the pure-Ruby WebAssembly
compositor. It is an **external client**: it runs in its own Web Worker as a
separate `js/wasm` instance and talks to the compositor only over the documented
[wire protocol](https://github.com/wasmdesk/wasmbox/blob/main/docs/protocol.md)
(`postMessage` + a `SharedArrayBuffer` pixel surface). The compositor owns the
canvas, stacking and input routing; the dock paints its own surface, posts
`commit` damage, and drives the desktop through `launch` / `focus` / `close`
messages.

## What it draws

A full-width, 28-pixel-tall **Fluxbox-style bottom toolbar** with a bevelled
gradient chrome, split into three sections that read left-to-right:

- **Workspace section** (left) — the active workspace as `"<active> of <count>"`.
  Click to cycle forward, scroll to cycle either way; kept in sync by the
  compositor's `workspace_changed` event.
- **Iconbar** (middle) — first the static **launchers** (one button per known
  app: `terminal` / `editor` / `files` / `hello`), a separator, then one
  **window button per open window** the compositor reports via `windows_changed`.
  Window buttons render in three Fluxbox styles — focused (sunken), unfocused
  open (raised), and minimized (`[*]` prefix). Left-click a window button to
  **focus/raise** it (restoring it if folded); right-click to **close** it.
- **Clock** (right) — `HH:MM`, ticked by the worker.

On top of that structure:

- **Hover magnification** — the launcher/window buttons under the cursor (and
  their neighbours, with a smooth falloff) swell macOS-style, and the row
  re-lays so hit-testing always maps through the magnified rectangles.
- **Running / active indicators** — a launcher whose app has an open window
  carries a "running" dot; the launcher of the focused window gets a brighter
  active underline.
- **Attention badges** — a count/alert pill drawn in a launcher's corner via the
  toolkit `Badge` widget (`SetBadge`).
- **Right-click context menus** — right-clicking a launcher or window entry opens
  a `launch` / `focus` / `close` menu in a child **popup** surface (the
  compositor's `popup` role), rendered with the toolkit `Menu` widget.

The built-in launcher set is a small, asset-free collection of drawn glyphs —
**terminal**, **editor**, **files**, **hello** (no external images, no
trademarked logos).

## Toolkit + theming

The bar is composed as a **[go-widgets/toolkit](https://github.com/go-widgets/toolkit)
widget tree** rather than hand-drawn rects: a `toolkit.HBox` with two fixed-width
end sections (workspace label + clock) and a flex iconbar container that owns a
`launcherButton` leaf per app and a `windowButton` leaf per window. Text is drawn
with the toolkit font (`DrawText`) and stock icons where they map cleanly
(`editor` → `DrawIconNew`, `files` → `DrawIconOpen`); the Fluxbox bevel/gradient
chrome and bespoke marks stay custom `Draw` over the painter primitives.

Chrome colours come from an **Openbox-style theme attribute tree**
(`internal/theme`) fed by a `.themerc` parser. Three themes ship — **fluxbox-dark**,
**fluxbox-light**, **gnome-adwaita** — and the active one is switchable at runtime
from the compositor's root menu.

## Architecture

All window-manager-independent logic — layout, magnification math, hit-testing,
indicators, menus, theming and the RGBA painting — lives in pure
`internal/scene` + `internal/theme` packages with **100% test coverage** and no
`syscall/js` dependency, so it builds and is unit-tested on any architecture. The
wasm `main.go` is the thin JS/SAB/`postMessage` glue, mirroring wasmbox's
reference `clients/hello` client.

```
main.go                       # js/wasm glue: SAB copy, input → scene, click → wire msg
internal/scene/scene.go       # pure toolbar renderer (layout / hit-test / paint)
internal/scene/magnify.go     # hover magnification (layout + falloff)
internal/scene/indicators.go  # running/active indicators + attention badges
internal/scene/menu.go        # right-click context-menu model + popup paint
internal/theme/               # Openbox-style theme tree + .themerc parser + 3 themes
sdk.js                        # worker-side wasmbox SDK (adapted, self-contained)
worker.js                     # worker bootloader: SDK + wasm_exec.js + dock.wasm
INTEGRATION.md                # the wasmbox-side extensions the dock uses (now landed)
```

## Build

Uses [Task](https://taskfile.dev):

```sh
task          # list the available tasks
task build    # GOOS=js GOARCH=wasm go build -o dock.wasm . + copy wasm_exec.js
task test     # go test ./...
task cover    # internal package coverage (100%)
task clean    # remove dock.wasm + wasm_exec.js
```

`wasmdock` is a Go module that builds for the `js/wasm` target with `CGO=0` and
`GOWORK=off`. To run it you serve `worker.js` + `dock.wasm` + `wasm_exec.js` from
a wasmbox page that spawns the dock as an external client (wasmbox's Taskfile
does this for you via `build:dock`). Because the surface is a
`SharedArrayBuffer`, the page must be served with the COOP/COEP
cross-origin-isolation headers (wasmbox's `cmd/serve` does this).

## Protocol & host integration

The wire contract is wasmbox's
[`docs/protocol.md`](https://github.com/wasmdesk/wasmbox/blob/main/docs/protocol.md).
The two wasmbox-side extensions the dock needs — a `launch` message handler and a
`"panel"` surface role — **have landed in wasmbox** (see `LAUNCHABLE` and the
`panel` role in `compositor/`), so the dock runs as a true always-on-top,
undecorated panel that launches and switches apps. [`INTEGRATION.md`](INTEGRATION.md)
documents those extensions and their trust boundary. The dock still **degrades
gracefully** on any host missing them: an unhandled `launch` is dropped and an
unknown role falls back to a normal window, so it always renders and logs.

## Part of [wasmdesk](https://github.com/wasmdesk)

`wasmdesk` is a family for a WebAssembly desktop built on pure-Go Ruby.
`wasmbox` is its compositor + window manager; `wasmdock` is the standalone dock /
toolbar client that drives it;
[`ociapps`](https://github.com/wasmdesk/ociapps) streams clients from any OCI
registry.
