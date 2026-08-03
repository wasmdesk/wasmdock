// SPDX-License-Identifier: BSD-3-Clause
//
// Command wasmdock is a Fluxbox-style bottom toolbar implemented as a
// wasmbox external client. It paints a full-width bevelled gray bar split into
// three sections — a workspace label, an iconbar of launcher buttons (with
// macOS-style hover magnification, running/active indicators + attention
// badges), and a clock — into the SAB the SDK allocated for it, and dispatches
// {type:"launch"/"focus"/"close", ...} to the compositor on clicks. A
// right-click on a launcher or window entry opens an application context menu
// in a child popup surface.
//
// The pure scene + theme packages do all the layout, hit-testing and painting;
// this file is the thin JS/SAB/postMessage glue. The worker.js shell posts a
// "tick" input event every 30 seconds carrying the current "HH:MM" string so
// the clock stays fresh.
//
//go:build js && wasm

package main

import (
	"encoding/json"
	"strings"
	"syscall/js"

	"github.com/wasmdesk/wasmdock/internal/scene"
	"github.com/wasmdesk/wasmdock/internal/theme"
)

// hasMethod reports whether the JS value exposes a callable method `name`. The
// standalone .verify harness stubs the client with only commit + onInput, so
// every optional SDK method (beginFrame / openPopup / requestClose) is
// feature-detected before use — a missing one degrades gracefully rather than
// throwing.
func hasMethod(v js.Value, name string) bool {
	return v.Get(name).Type() == js.TypeFunction
}

// exposeGeometry publishes the dock's current launcher + window-button
// rectangles (MAGNIFIED when the cursor hovers, so a probe clicks where the
// button is actually painted) on a worker global for headless probes. Read via
// worker.evaluate(() => globalThis.__wasmdockGeometry); the screen position of
// a rect is (VIEW_W - w)/2 + x, (VIEW_H - h) + y (the dock is bottom-center
// anchored). Cheap; refreshed on window changes + hover repaints.
func exposeGeometry(state *scene.State) {
	wr := state.WindowRects()
	buttons := make([]interface{}, 0, len(wr))
	for i, r := range wr {
		w := state.Windows[i]
		buttons = append(buttons, map[string]interface{}{
			"id": w.Id, "title": w.Title, "minimized": w.Minimized,
			"focused": w.Focused, "x": r[0], "y": r[1], "w": r[2], "h": r[3],
		})
	}
	lr := state.LauncherRects()
	launchers := make([]interface{}, 0, len(lr))
	for i, r := range lr {
		launchers = append(launchers, map[string]interface{}{
			"id": state.Apps[i].Id, "x": r[0], "y": r[1], "w": r[2], "h": r[3],
		})
	}
	js.Global().Set("__wasmdockGeometry", js.ValueOf(map[string]interface{}{
		"w": state.W, "h": state.H, "buttons": buttons, "launchers": launchers,
	}))
}

func main() {
	client := js.Global().Get("wasmboxClient")
	if client.IsUndefined() {
		println("wasmdock: wasmboxClient missing; SDK not loaded?")
		return
	}

	w := client.Get("w").Int()
	h := client.Get("h").Int()
	pixels := client.Get("pixels")
	bufLen := pixels.Get("length").Int()
	if bufLen != 4*w*h {
		println("wasmdock: pixel buffer size mismatch")
		return
	}

	// Pure-Go RGBA buffer; scene.Render fills it, then we copy once per frame
	// into the SAB through the SDK's Uint8ClampedArray view.
	local := make([]byte, 4*w*h)
	state := scene.New(w, h)

	render := func() {
		scene.Render(state, local)
		// Open the seqlock write window before copying the frame into the SAB
		// so the compositor never blits a half-copied frame (matters for the
		// per-hover magnification repaints). commit() closes it.
		if hasMethod(client, "beginFrame") {
			client.Call("beginFrame")
		}
		js.CopyBytesToJS(pixels, local)
		damage := js.Global().Call("Object")
		damage.Set("x", 0)
		damage.Set("y", 0)
		damage.Set("w", w)
		damage.Set("h", h)
		client.Call("commit", damage)
	}

	launch := func(app string) { client.Call("launch", app) }
	focusWin := func(id int) { client.Call("focus", id) }

	// setWorkspace asks the compositor to switch the active workspace.
	setWorkspace := func(index int) { client.Call("setWorkspace", index) }

	// Initial paint so the compositor has something to blit immediately, plus a
	// first geometry publish so a probe can read the resting layout.
	render()
	exposeGeometry(state)

	cb := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) == 0 {
			return nil
		}
		ev := args[0]
		kind := ev.Get("kind").String()
		switch kind {
		case "mousemove":
			x := ev.Get("x").Int()
			y := ev.Get("y").Int()
			state.SetCursor(x, y, true)
			// Repaint so the hover magnification follows the cursor. Skipped
			// when the effect is off so a flat dock stays idle while the cursor
			// wanders.
			if state.Magnify.On {
				render()
				exposeGeometry(state)
			}
		case "mouseleave", "mouseout":
			// Pointer left the panel — drop the magnification back to flat.
			state.SetCursor(state.CursorX, state.CursorY, false)
			if state.Magnify.On {
				render()
				exposeGeometry(state)
			}
		case "mousedown":
			x := ev.Get("x").Int()
			y := ev.Get("y").Int()
			// Keep the cursor recorded so hit-testing reads the SAME magnified
			// geometry the user is clicking on.
			state.SetCursor(x, y, true)
			// Mouse button: 0 = left, 2 = right (W3C DOM MouseEvent.button).
			button := 0
			if b := ev.Get("button"); !b.IsUndefined() && !b.IsNull() {
				button = b.Int()
			}
			// Workspace section: left-click cycles to the next workspace; a
			// right-click there is reserved for a future workspace menu.
			if state.HitTestWorkspace(x, y) {
				if button != 2 {
					setWorkspace(state.NextWorkspace())
				}
				break
			}
			if i := state.HitTest(x, y); i >= 0 {
				if button == 2 {
					openMenu(client, state.BuildLauncherMenu(i), x)
				} else {
					launch(state.Apps[i].Id)
				}
				break
			}
			if i := state.HitTestWindow(x, y); i >= 0 {
				if button == 2 {
					openMenu(client, state.BuildWindowMenu(i), x)
				} else {
					// Left-click focuses + raises (restoring if minimized).
					focusWin(state.Windows[i].Id)
				}
			}
		case "wheel":
			// Scroll-wheel over the workspace section cycles workspaces.
			x := ev.Get("x").Int()
			y := ev.Get("y").Int()
			if !state.HitTestWorkspace(x, y) {
				break
			}
			dy := 0.0
			if d := ev.Get("deltaY"); !d.IsUndefined() && !d.IsNull() {
				dy = d.Float()
			}
			if dy > 0 {
				setWorkspace(state.NextWorkspace())
			} else if dy < 0 {
				setWorkspace(state.PrevWorkspace())
			}
		case "workspace_changed":
			if c := ev.Get("count"); !c.IsUndefined() && !c.IsNull() {
				state.SetWorkspaceCount(c.Int())
			}
			if a := ev.Get("active"); !a.IsUndefined() && !a.IsNull() {
				state.SetActiveWorkspace(a.Int())
			}
			render()
		case "windows_changed":
			raw := ev.Get("windows_json")
			if raw.IsUndefined() || raw.IsNull() {
				state.SetWindows(nil)
			} else {
				var parsed []scene.Window
				if err := json.Unmarshal([]byte(raw.String()), &parsed); err != nil {
					println("wasmdock: windows_changed parse error:", err.Error())
					parsed = nil
				}
				state.SetWindows(parsed)
			}
			render()
			exposeGeometry(state) // test hook: publish button rects
		case "tick":
			clock := ev.Get("clock")
			if !clock.IsUndefined() && !clock.IsNull() {
				state.SetClock(clock.String())
			}
			ws := ev.Get("workspace")
			if !ws.IsUndefined() && !ws.IsNull() {
				state.SetWorkspace(ws.String())
			}
			render()
		case "theme_changed":
			rc := ev.Get("themerc")
			if rc.IsUndefined() || rc.IsNull() {
				break
			}
			src := rc.String()
			if strings.TrimSpace(src) == "" {
				break
			}
			th, _ := theme.ParseRC(strings.NewReader(src))
			state.SetTheme(th)
			render()
		}
		return nil
	})
	client.Call("onInput", cb)

	// Park forever so the Go runtime keeps the FuncOf callback alive.
	select {}
}

// openMenu opens the dock's right-click application context menu in a child
// popup surface anchored above the clicked entry, paints it via the pure
// scene.DockMenu renderer, and routes a click inside it back to a launch /
// focus / close wire message. The 28px bar is far too short to draw a menu
// in-surface, so this uses the compositor's existing "popup" role (no
// compositor change): SetCursor-consistent hit-testing picks the entry, the
// compositor grab-dismisses the popup on an outside click, and a selection
// requests the popup's close. A no-op when the menu is empty or the SDK has no
// openPopup (the .verify harness stub), so the dock never throws.
func openMenu(client js.Value, menu scene.DockMenu, anchorX int) {
	if len(menu.Entries) == 0 || !hasMethod(client, "openPopup") {
		return
	}
	// Anchor the menu above the bar (rel_y negative pops it upward), centred
	// under the click and clamped to the parent's left edge.
	relX := anchorX - menu.W/2
	if relX < 0 {
		relX = 0
	}
	opts := js.Global().Call("Object")
	opts.Set("title", "dock menu")
	opts.Set("w", menu.W)
	opts.Set("h", menu.H)
	opts.Set("rel_x", relX)
	opts.Set("rel_y", -menu.H)
	popup := client.Call("openPopup", opts)

	hover := -1
	var buf []byte
	var pw, ph int
	var pixels js.Value

	paint := func() {
		if buf == nil {
			return
		}
		menu.MenuRender(buf, pw, ph, hover)
		if hasMethod(popup, "beginFrame") {
			popup.Call("beginFrame")
		}
		js.CopyBytesToJS(pixels, buf)
		popup.Call("commit", js.Undefined())
	}

	var inputCb js.Func
	popup.Call("onWelcome", js.FuncOf(func(_ js.Value, _ []js.Value) any {
		pw = popup.Get("w").Int()
		ph = popup.Get("h").Int()
		pixels = popup.Get("pixels")
		buf = make([]byte, 4*pw*ph)
		paint()
		return nil
	}))
	inputCb = js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) == 0 {
			return nil
		}
		ev := args[0]
		switch ev.Get("kind").String() {
		case "mousemove":
			if hv := menu.MenuHover(ev.Get("y").Int()); hv != hover {
				hover = hv
				paint()
			}
		case "mousedown":
			if idx := menu.MenuHitTest(ev.Get("y").Int()); idx >= 0 {
				dispatchMenu(client, menu.Entries[idx])
			}
			// Dismiss the menu after a click (a selection or a gap click).
			if hasMethod(popup, "requestClose") {
				popup.Call("requestClose")
			}
		}
		return nil
	})
	popup.Call("onInput", inputCb)
	popup.Call("onClosed", js.FuncOf(func(this js.Value, _ []js.Value) any {
		inputCb.Release()
		return nil
	}))
}

// dispatchMenu sends the wire message a chosen menu entry maps to.
func dispatchMenu(client js.Value, e scene.MenuEntry) {
	switch e.Action {
	case scene.ActLaunch:
		client.Call("launch", e.App)
	case scene.ActFocus:
		client.Call("focus", e.Win)
	case scene.ActClose:
		client.Call("closeWindow", e.Win)
	}
}
