# wasmbox-side integration for wasmdock

`wasmdock` is an **external client** of the [`wasmbox`](https://github.com/wasmdesk/wasmbox)
compositor. It speaks the existing step-B protocol (`hello` / `welcome` /
`commit` / `input` / `set_title` / `request_close` / `closed`) verbatim, so it
runs **today** as an ordinary window with zero compositor changes — it renders
the dock, magnifies on `mousemove`, and logs a launch request on click.

To make the dock a *useful* dock, the compositor needs two small additions.
Both are designed so the dock **degrades gracefully** when they are absent: an
unknown `launch` message is dropped, and an unknown `role` falls back to a
normal window. This document specifies exactly what to add **wasmbox-side**;
do not change wasmdock for these.

The Ruby touch-points named below match the wasmbox README/protocol doc:
`WindowManager#handle_client_message`, `ExternalWindow`, and `Window`.

---

## 1. The `launch` message handler (C → S)

### Wire format

The dock posts, on icon click:

```js
{ type: "launch", app: "<id>" }
```

* `app` is one of the dock's built-in app ids: `"terminal"`, `"editor"`,
  `"files"`. Treat it as an opaque, validated string key — never a path or a
  command line.
* The message carries **no `window_id`**: it is a request to create a *new*
  client, not an operation on the sender's window.

### Required behavior

Add a `"launch"` case to `WindowManager#handle_client_message` (the same
dispatch that today handles `hello` / `commit` / `set_title` /
`request_close`):

1. **Validate** `app` against an allow-list / registry mapping each id to a
   known client worker URL, e.g.

   ```ruby
   LAUNCHABLE = {
     "terminal" => "clients/terminal/worker.js",
     "editor"   => "clients/editor/worker.js",
     "files"    => "clients/files/worker.js",
   }.freeze
   ```

   If `app` is not in the registry, **ignore** the message (optionally log).
   Never spawn anything from an unrecognized id.

2. **Spawn** the mapped client exactly as the compositor already spawns its
   own external clients — construct a `Worker(worker_url)` and route its
   subsequent `hello` through the normal `spawn_external` path so it gets a
   `window_id`, a SAB, placement and focus like any other client.

3. **Optional idempotency / singleton policy**: the compositor may choose to
   focus an already-running instance of `app` instead of spawning a second
   one. This is policy, not protocol; the dock does not care either way.

4. **No reply is required.** The dock is fire-and-forget. (A future
   `launched { app, window_id }` ack would let the dock show a "running"
   indicator dot under the icon, but it is optional and out of scope here.)

### Security note

The registry is the trust boundary. Because `app` is validated against a fixed
map of compositor-owned worker URLs, a malicious client cannot use `launch` to
run arbitrary code — at worst it can ask to open one of the already-installed
clients. Do **not** let `launch` carry a URL, path, or argv.

---

## 2. The `"panel"` surface role

### Wire format

The dock sends its role in the existing `hello` message (the SDK adds the
field; older compositors ignore unknown keys):

```js
{ type: "hello", title: "wasmdock", role: "panel", w: 480, h: 120,
  sab: SharedArrayBuffer, stride: 1920 }
```

`role` is an optional string. When absent or unrecognized it MUST default to
the current behavior (`"window"`).

### Required behavior

When `WindowManager.spawn_external` (or whatever `hello` handles) sees
`role == "panel"`, the resulting `ExternalWindow` / `Window` must differ from a
normal window in these specific ways:

| Aspect            | Normal window                         | `"panel"` role                                              |
| ----------------- | ------------------------------------- | ----------------------------------------------------------- |
| **Position**      | Cascade placement                     | **Anchored**: bottom-center of the desktop (see below)      |
| **Stacking**      | Click-to-raise within the normal pool | **Always-on-top**: kept above all normal windows every frame|
| **Decoration**    | Compositor draws titlebar + close box | **None**: no titlebar, no close box, no resize grip         |
| **Drag**          | Titlebar drag moves it                | **Not draggable**: titlebar hit-testing is disabled         |
| **Resize**        | Bottom-right grip resizes             | **Not resizable** (the dock owns its own fixed surface)     |
| **Focus cycle**   | Included in Alt-Tab                   | **Excluded** from the Alt-Tab / focus-cycle ring            |
| **Input**         | Forwarded when focused                | Forwarded on **hover** (so magnification tracks the cursor) |

Concretely, the minimal wasmbox changes:

1. **Carry the role.** Store `role` on `ExternalWindow` (default `"window"`).
   Plumb it through `spawn_external` from the `hello` payload.

2. **Anchored placement.** For a panel, skip cascade placement. Each frame (or
   on viewport resize) set:

   ```
   x = (canvas_w - surface_w) / 2     # horizontally centered
   y =  canvas_h - surface_h          # flush to the bottom edge
   ```

   The dock paints its own bottom margin inside the surface, so anchor the
   surface's bottom to the canvas bottom. Because the panel is undecorated, the
   surface *is* the whole window (no titlebar offset).

3. **No decorations.** In `Window`'s decoration/draw path, when `role ==
   "panel"` draw no titlebar, no close box, no resize corner; the body
   rectangle equals the surface rectangle. Decoration hit-tests (titlebar drag,
   close box, resize grip) must all return "no hit" for panels so those gestures
   never start.

4. **Always-on-top.** Keep panels in a separate top stratum of the stacking
   order: when compositing bottom-to-top, draw all normal windows first, then
   all panels. New normal windows must never raise above a panel.

5. **Exclude from focus cycle.** In the Alt-Tab cycle and click-to-focus
   raise-policy, skip panels (the dock should not steal Tab focus from app
   windows). Still route `input` to the panel when the pointer is **over** it,
   so the dock receives `mousemove` for magnification and `mousedown` for
   clicks even though it is not the keyboard-focused window. (Today's protocol
   forwards input to the focused window; for panels, forward pointer events on
   geometric hover instead.)

6. **Transparency.** The dock leaves the area outside its rounded bar fully
   transparent (alpha 0). The compositor's blit already uses `putImageData`
   straight from the SAB, which preserves the alpha channel, so the corners
   show the desktop through them. No extra work is required beyond *not*
   drawing an opaque window background behind a panel.

### Why a role rather than per-flag messages

Bundling these behaviors under one `role` keeps the protocol small and makes
the contract explicit: a "panel" is a well-known kind of surface (dock, top
bar, notification shelf) with a fixed bundle of semantics, rather than seven
independent toggles a client must set correctly.

---

## Summary of the two changes

1. **`launch` handler** in `WindowManager#handle_client_message`: validate
   `app` against a compositor-owned registry, spawn the mapped client worker
   through the existing external-client path, drop unknown ids. No reply.

2. **`"panel"` role** plumbed from `hello` onto `ExternalWindow`/`Window`:
   bottom-center anchored, always-on-top, undecorated, not draggable, not
   resizable, excluded from Alt-Tab, pointer-forwarded on hover, alpha
   preserved. Unknown/absent role → normal window.

Until these land, wasmdock runs as a normal draggable window that renders the
dock and logs `wasmdock: launch <app>` on click.
