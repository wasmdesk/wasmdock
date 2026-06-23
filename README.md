<h1 align="center">wasmdock</h1>
<p align="center"><strong>A macOS-style dock for the wasmdesk WebAssembly desktop.</strong></p>

<p align="center">
  A bottom-anchored, translucent, magnifying dock — a standalone
  <a href="https://github.com/wasmdesk/wasmbox">wasmbox</a> external client,
  written in pure Go (CGO=0) and rendered into a shared surface.
</p>

<p align="center">
  <a href="https://github.com/wasmdesk"><img alt="part of wasmdesk" src="https://img.shields.io/badge/wasmdesk-the%20WASM%20desktop-1a7f37?style=flat-square"></a>
  <a href="https://github.com/wasmdesk/wasmbox"><img alt="client of wasmbox" src="https://img.shields.io/badge/client%20of-wasmbox-9B1C2E?style=flat-square"></a>
  <img alt="WebAssembly" src="https://img.shields.io/badge/WebAssembly-CGO%3D0-654FF0?style=flat-square&logo=webassembly&logoColor=white">
  <a href="LICENSE"><img alt="License: BSD-3-Clause" src="https://img.shields.io/badge/license-BSD--3--Clause-blue?style=flat-square"></a>
</p>

---

`wasmdock` is a **dock client** for [`wasmbox`](https://github.com/wasmdesk/wasmbox),
the pure-Ruby WebAssembly compositor. It is an **external client**: it runs in
its own Web Worker as a separate `js/wasm` instance and talks to the compositor
only over the documented
[step-B protocol](https://github.com/wasmdesk/wasmbox/blob/main/docs/protocol.md)
(`postMessage` + a `SharedArrayBuffer` pixel surface). The compositor owns the
canvas, stacking and input routing; the dock only paints its own surface and
posts `commit` damage.

## What it draws

- A **horizontally-centered, bottom-anchored translucent rounded bar** with a
  row of app icons, painted onto an otherwise-transparent surface so the
  desktop shows through the rounded corners.
- **Hover magnification**: on each `input` `mousemove` the icons around the
  cursor scale up (macOS genie-style cosine falloff), peaking on the icon under
  the pointer and decaying to rest with distance.
- **Click to launch**: a `mousedown` on an icon hit-tests the magnified row and
  posts a `{ type: "launch", app: "<id>" }` message asking the compositor to
  start that app.

The built-in app set is a small, asset-free collection of drawn glyphs —
**terminal**, **editor**, **files** (no external images, no trademarked logos).

## Architecture

All window-manager-independent logic — icon layout, magnification math,
hit-testing and the RGBA painting — lives in a **pure `internal/scene`
package** with **100% test coverage** and no `syscall/js` dependency, so it
builds and is unit-tested on any architecture. The wasm `main.go` is the thin
JS/SAB/`postMessage` glue, mirroring wasmbox's reference `clients/hello` client.

```
main.go                       # js/wasm glue: SAB copy, input → scene, click → launch
internal/scene/scene.go       # pure dock renderer (layout/magnify/hit-test/paint)
internal/scene/scene_test.go  # 100% coverage
sdk.js                        # worker-side wasmbox SDK (adapted, self-contained)
worker.js                     # worker bootloader: SDK + wasm_exec.js + dock.wasm
INTEGRATION.md                # the two wasmbox-side extensions the dock needs
```

## Build

Uses [Task](https://taskfile.dev):

```sh
task          # list the available tasks
task build    # GOOS=js GOARCH=wasm go build -o dock.wasm . + copy wasm_exec.js
task test     # go test ./...
task cover    # internal/scene coverage (100%)
task clean    # remove dock.wasm + wasm_exec.js
```

`wasmdock` is a Go module that builds for the `js/wasm` target with `CGO=0` and
`GOWORK=off`. To run it you serve `worker.js` + `dock.wasm` + `wasm_exec.js`
from a wasmbox page that spawns the dock as an external client. Because the
surface is a `SharedArrayBuffer`, the page must be served with the COOP/COEP
cross-origin-isolation headers (wasmbox's `cmd/serve` does this).

## Protocol & host integration

The wire contract is wasmbox's
[`docs/protocol.md`](https://github.com/wasmdesk/wasmbox/blob/main/docs/protocol.md).
The dock runs **today** as an ordinary window with no compositor changes. To
become a true always-on-top, undecorated dock that can launch other clients, it
needs two small **wasmbox-side** additions — a `launch` message handler and a
`"panel"` surface role — fully specified in [`INTEGRATION.md`](INTEGRATION.md).
The dock **degrades gracefully** without them: an unhandled `launch` is dropped
and an unknown role falls back to a normal window, so it still renders and logs.

## Part of [wasmdesk](https://github.com/wasmdesk)

`wasmdesk` is a family for a WebAssembly desktop built on pure-Go Ruby.
`wasmbox` is its compositor + window manager; `wasmdock` is a standalone dock
client that drives it.
