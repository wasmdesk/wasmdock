// SPDX-License-Identifier: BSD-3-Clause

package scene

import (
	"github.com/go-widgets/painter"
	"github.com/go-widgets/toolkit"
)

// Right-click application context menu.
//
// The 28px toolbar is far too short to draw a multi-row menu in its own
// surface, so a right-click on a launcher or window entry opens the menu in a
// child POPUP surface (the compositor's existing "popup" role — undecorated,
// grab-dismissed on an outside click). This file is the pure model + paint of
// that popup: the wasm shell asks scene for the menu that a given click should
// show (BuildLauncherMenu / BuildWindowMenu), sizes + opens the popup, paints
// it here through the toolkit Menu widget (MenuRender), and routes a click
// inside it back to a wire message via MenuHitTest.
//
// Every entry maps onto one of the three wire messages the dock already speaks
// — launch / focus / close — so the popup adds no new compositor protocol.

// MenuAction is the wire message a menu entry dispatches when chosen.
type MenuAction int

const (
	// ActNone is a disabled / separator entry: choosing it does nothing.
	ActNone MenuAction = iota
	// ActLaunch sends {launch, app:App} — a fresh instance of the launcher.
	ActLaunch
	// ActFocus sends {focus, id:Win} — raise + focus (restoring if folded).
	ActFocus
	// ActClose sends {close, id:Win} — close the window.
	ActClose
)

// MenuEntry is one row of a dock context menu. Separator rows draw a divider
// and are never selectable. App is the launcher id for ActLaunch; Win is the
// compositor window id for ActFocus / ActClose.
type MenuEntry struct {
	Label     string
	Action    MenuAction
	App       string
	Win       int
	Separator bool
}

// DockMenu is a built, ready-to-open context menu: the entries plus the popup
// surface size (W x H) they need. The wasm shell reads W/H to open the popup at
// the right size and MenuRender paints into that surface.
type DockMenu struct {
	Entries []MenuEntry
	W, H    int
}

// MenuMinW is the floor popup width so a short label ("Show") still yields a
// comfortably clickable menu.
const MenuMinW = 120

// menuPad is the horizontal breathing room added to the widest label to size
// the popup (matches the toolkit Menu's 8px label inset on each side).
const menuPad = 16

// BuildLauncherMenu builds the context menu for a right-click on the launcher
// with index i: always "New Window" (launch a fresh instance); when the app is
// already running, a separator then "Show" (focus its first window) and
// "Close" (close its first window). Returns an empty menu (nil entries) for an
// out-of-range index so the caller opens nothing.
func (s *State) BuildLauncherMenu(i int) DockMenu {
	if i < 0 || i >= len(s.Apps) {
		return DockMenu{}
	}
	app := s.Apps[i]
	entries := []MenuEntry{{Label: "New Window", Action: ActLaunch, App: app.Id}}
	// First window that maps to this launcher, if any.
	firstWin, running := -1, false
	for _, w := range s.Windows {
		if s.appIndexForWindow(w) == i {
			running = true
			firstWin = w.Id
			break
		}
	}
	if running {
		entries = append(entries,
			MenuEntry{Separator: true},
			MenuEntry{Label: "Show", Action: ActFocus, Win: firstWin},
			MenuEntry{Label: "Close", Action: ActClose, Win: firstWin},
		)
	}
	return finishMenu(entries)
}

// BuildWindowMenu builds the context menu for a right-click on the open-window
// entry with index i: "Show" (focus + raise, restoring if folded) and "Close".
// Returns an empty menu for an out-of-range index.
func (s *State) BuildWindowMenu(i int) DockMenu {
	if i < 0 || i >= len(s.Windows) {
		return DockMenu{}
	}
	w := s.Windows[i]
	entries := []MenuEntry{
		{Label: "Show", Action: ActFocus, Win: w.Id},
		{Label: "Close", Action: ActClose, Win: w.Id},
	}
	return finishMenu(entries)
}

// finishMenu computes the popup size for a set of entries: width from the
// widest label (floored at MenuMinW), height from the toolkit row/separator
// metrics plus the 2px top+bottom body inset the toolkit Menu draws.
func finishMenu(entries []MenuEntry) DockMenu {
	w := MenuMinW
	h := 4 // 2px top + 2px bottom body inset
	for _, e := range entries {
		if e.Separator {
			h += toolkit.MenuSeparatorH
			continue
		}
		if lw := toolkit.TextWidth(e.Label) + menuPad; lw > w {
			w = lw
		}
		h += toolkit.MenuRowH
	}
	return DockMenu{Entries: entries, W: w, H: h}
}

// toolkitMenu converts the dock menu into a bounded toolkit.Menu with the
// hovered row set on its Hover() observable. Selectable entries get a non-nil
// Action so the widget draws them enabled + hoverable AND so Menu.RowAt resolves
// them; separators map to Separator rows and a disabled (ActNone) entry keeps a
// nil Action so Menu.RowAt reports it as un-hittable — exactly the rows the dock
// dispatches. The func bodies are empty: the dock routes the real dispatch
// through MenuHitTest, not the widget's own callback (the popup is paint-only).
//
// The menu is bounded to the popup's W x H so its own Menu.RowAt / Hover row
// math is authoritative — the dock no longer mirrors MenuRowH / body-inset /
// separator metrics by hand.
func (m DockMenu) toolkitMenu(hover int) *toolkit.Menu {
	items := make([]toolkit.MenuItem, 0, len(m.Entries))
	for _, e := range m.Entries {
		switch {
		case e.Separator:
			items = append(items, toolkit.MenuItem{Separator: true})
		case e.Action == ActNone:
			// Disabled row: no Action, so Menu.RowAt returns -1 for it.
			items = append(items, toolkit.MenuItem{Label: e.Label})
		default:
			items = append(items, toolkit.MenuItem{Label: e.Label, Action: func() {}})
		}
	}
	tm := toolkit.NewMenu(items)
	tm.SetBounds(toolkit.Rect{X: 0, Y: 0, W: m.W, H: m.H})
	tm.Hover().Set(hover)
	return tm
}

// MenuRender paints the menu into buf (a 4*W*H RGBA popup surface) with the row
// at hover index highlighted (-1 = none). It renders through the toolkit Menu
// widget so the popup matches the compositor's own menus. buf must be exactly
// 4*W*H bytes.
func (m DockMenu) MenuRender(buf []byte, w, h, hover int) {
	if len(buf) != 4*w*h {
		panic("scene: menu buffer size mismatch")
	}
	p := painter.NewPixelPainter(buf, w, h)
	tm := m.toolkitMenu(hover)
	tm.Draw(p, menuTheme)
}

// MenuHitTest returns the entry index at popup-relative y (x is unused — menu
// rows span the full width), or -1 for a separator, a disabled row or a click
// outside any row. It defers to the toolkit Menu's own Menu.RowAt so the popup
// resolves a click through the SAME row geometry the widget paints (row height,
// body inset, separator height, scale + touch density) rather than re-deriving
// it — no hand-rolled row math to drift out of sync.
func (m DockMenu) MenuHitTest(y int) int {
	return m.toolkitMenu(-1).RowAt(0, y)
}

// MenuHover returns the entry index that a pointer at popup-relative y hovers,
// or -1 when it is over a separator / gap. Shares Menu.RowAt with MenuHitTest so
// the highlight passed to MenuRender always tracks the row a click would act on.
func (m DockMenu) MenuHover(y int) int { return m.MenuHitTest(y) }

// menuTheme is the toolkit Theme the popup menu draws with — the toolkit's
// default light scheme, which gives the Menu widget a surface body, a border,
// an accent hover bar and readable ink without pulling the dock's Openbox theme
// (whose gradient title colours are not a menu palette).
var menuTheme = toolkit.DefaultLight()
