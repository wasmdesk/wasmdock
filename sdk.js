// wasmbox client SDK (worker side), adapted for wasmdock.
//
// This is a self-contained copy of the wasmbox SDK pattern so the dock does
// not depend on a path inside the wasmbox checkout. A wasmbox external client
// lives in a Web Worker; this SDK is what the worker imports to talk to the
// compositor.
//
// It allocates the surface SharedArrayBuffer, posts the initial `hello`, waits
// for `welcome`, exposes a `commit(damage)` flusher, dispatches incoming
// `input` events to a user-supplied callback, and adds a small `launch(app)`
// helper for the dock's launch protocol extension (see INTEGRATION.md). If the
// compositor does not implement `launch` yet, the message is simply ignored on
// the host side — the dock keeps rendering.
//
// See the wasmbox docs/protocol.md for the wire format.
//
// Usage (inside the worker):
//   importScripts("./sdk.js");
//   const client = new WasmboxClient({ title: "dock", w: 480, h: 120 });
//   client.onWelcome((info) => { ... paint, then client.commit(); });
//   client.onInput((event) => { ... });
//   client.start();
//
// The wasm Go program (loaded after `client.start()` resolves) reaches the SDK
// through `globalThis.wasmboxClient` (set by the worker bootloader).

"use strict";

(function (g) {
  class WasmboxClient {
    constructor(opts) {
      const w = opts.w | 0;
      const h = opts.h | 0;
      if (!w || !h) throw new Error("WasmboxClient requires positive w + h");
      this.title = opts.title || "client";
      this.role = opts.role || "window"; // dock requests the "panel" role
      this.w = w;
      this.h = h;
      this.stride = 4 * w;
      // 4 bytes per pixel (RGBA32), row-major, origin top-left.
      this.sab = new SharedArrayBuffer(this.stride * h);
      this.pixels = new Uint8ClampedArray(this.sab); // worker-side view
      this.windowId = null;
      this._welcomeCbs = [];
      this._inputCbs = [];
      this._closedCbs = [];
      // Buffer for input events that arrived BEFORE any onInput handler was
      // attached. The Go side only calls onInput after its wasm boots, which
      // can race a `windows_changed` snapshot the compositor posts in the
      // welcome handler — without this buffer the initial snapshot would be
      // silently dropped. Flushed (in FIFO order) by the first onInput()
      // call.
      this._pendingInputs = [];
      this._onMessage = (e) => this._handle(e.data);
    }

    // Begin listening + post hello. Returns a Promise that resolves with the
    // welcome payload (so the client can `await client.start()` and then paint).
    start() {
      g.addEventListener("message", this._onMessage);
      g.postMessage({
        type: "hello",
        title: this.title,
        role: this.role, // panel role (compositor may ignore → defaults to window)
        w: this.w,
        h: this.h,
        sab: this.sab,
        stride: this.stride,
      });
      return new Promise((resolve) => this.onWelcome(resolve));
    }

    onWelcome(fn) { this._welcomeCbs.push(fn); }
    onInput(fn)   {
      this._inputCbs.push(fn);
      // Drain any input events that arrived before the first onInput handler
      // was attached — see _pendingInputs in the constructor.
      if (this._pendingInputs.length) {
        const queued = this._pendingInputs;
        this._pendingInputs = [];
        for (const ev of queued) fn(ev);
      }
    }
    onClosed(fn)  { this._closedCbs.push(fn); }

    // Tell the compositor "I have new pixels". `damage` defaults to the full
    // surface.
    commit(damage) {
      if (this.windowId === null) return;
      const d = damage || { x: 0, y: 0, w: this.w, h: this.h };
      g.postMessage({
        type: "commit",
        window_id: this.windowId,
        damage: d,
      });
    }

    setTitle(title) {
      this.title = title;
      if (this.windowId === null) return;
      g.postMessage({ type: "set_title", window_id: this.windowId, title: title });
    }

    requestClose() {
      if (this.windowId === null) return;
      g.postMessage({ type: "request_close", window_id: this.windowId });
    }

    // launch asks the compositor to start another client. Protocol extension
    // (see INTEGRATION.md). Fire-and-forget: if the host has no handler the
    // message is dropped and the dock keeps working.
    launch(app) {
      g.postMessage({ type: "launch", app: String(app) });
    }

    // restore asks the compositor to un-minimize a folded window. Kept as
    // an alias of focus for backward compatibility with the existing wire
    // protocol; the compositor's `:focus` arm now restores minimized
    // windows on its own. Fire-and-forget.
    restore(id) {
      g.postMessage({ type: "restore", window_id: id | 0 });
    }

    // focus asks the compositor to raise + focus a window the user clicked
    // on its iconbar button. If the window is minimized it is restored
    // first. Fire-and-forget.
    focus(id) {
      g.postMessage({ type: "focus", window_id: id | 0 });
    }

    // closeWindow asks the compositor to close a window the user
    // right-clicked on its iconbar button. Same effect as clicking the
    // window's title-bar close box. Fire-and-forget. (Named closeWindow,
    // not close, because `close()` is taken on the global Worker scope.)
    closeWindow(id) {
      g.postMessage({ type: "close", window_id: id | 0 });
    }

    // setWorkspace asks the compositor to switch the active workspace to
    // `index` (1..workspaceCount, currently 4). The compositor drops
    // out-of-range or already-active indices and broadcasts a
    // `workspace_changed` input event back to every panel on success.
    // Fire-and-forget.
    setWorkspace(index) {
      g.postMessage({ type: "set_workspace", index: index | 0 });
    }

    // --- internals -------------------------------------------------------
    _handle(msg) {
      if (!msg || typeof msg.type !== "string") return;
      switch (msg.type) {
        case "welcome":
          this.windowId = msg.window_id;
          this.w = msg.granted_w | 0;
          this.h = msg.granted_h | 0;
          this.stride = 4 * this.w;
          for (const fn of this._welcomeCbs) fn(msg);
          break;
        case "input":
          if (this._inputCbs.length) {
            for (const fn of this._inputCbs) fn(msg.event || {});
          } else {
            // No onInput handler yet (wasm not booted) — buffer the event so
            // the first onInput() call drains it. See _pendingInputs.
            this._pendingInputs.push(msg.event || {});
          }
          break;
        case "closed":
          for (const fn of this._closedCbs) fn(msg.reason || "user");
          g.removeEventListener("message", this._onMessage);
          break;
      }
    }
  }

  g.WasmboxClient = WasmboxClient;
})(self);
