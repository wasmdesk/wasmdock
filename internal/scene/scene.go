// SPDX-License-Identifier: BSD-3-Clause
//
// Package scene paints the wasmdock surface as a Fluxbox-style bottom toolbar:
// a full-width, 28-pixel-tall bevelled gray bar carrying —
//
//   - an iconbar of application launchers: a toolkit.AppDock (the modern,
//     grouped-window dock) with one icon item per known LAUNCHER (terminal /
//     editor / files / hello) drawn from the iconoir icon family. Open windows
//     do NOT get their own buttons — they collapse into indicators on their
//     launcher: a "running" dot when the app has at least one open window, an
//     accent-filled face for the launcher whose window holds focus, and an
//     attention badge for the count. Left-clicking a launcher posts a `launch`
//     message; per-window actions (focus / close) are reached through the
//     right-click application menu;
//   - a workspace pager pinned at the trailing (right) end: a
//     toolkit.WorkspacePager of one cell per workspace, the active one
//     highlighted, an occupancy dot on the workspaces that hold windows.
//     Clicking a cell switches to it; the active workspace is reported by the
//     compositor through the `workspace_changed` input event;
//   - a clock ("HH:MM") at the far trailing end: a toolkit.Clock kept in sync by
//     a `tick` event posted by the JS worker.
//
// # Toolkit widget model
//
// The bar is ONE toolkit.DockPanel — the shared dock shell — rather than a
// hand-composed tree of section leaves around a bare AppDock: the DockPanel
// wraps the AppDock and pins the WorkspacePager + Clock as trailing accessory
// widgets, laying them out, confining the dock's hover magnification to its own
// run and (would, if set) owning the context menu. A single themed
// toolkit.Backdrop paints the bar's Fluxbox face (gradient + raised bevel)
// under the panel and a 1-pixel toolkit.Backdrop paints the top-border strip, so
// no hand-drawn shape-painting is left in the toolbar and every visible element
// is a toolkit widget.
//
// The widget tree is built ONCE (ensureView) and cached on the State; every
// frame syncView binds the live model onto it — the clock text through the
// Clock's Func seam, the workspace state onto the pager's Count / Occupied /
// Current, and the dock's indicators / cursor through the AppDock's own setters —
// rather than reallocating widgets, so a hover repaint or a windows_changed
// event constructs no tree. The pure *Rect / HitTest* geometry methods read the
// panel's laid-out widget bounds, so paint, hit-testing and the probe hook all
// share one layout.
//
// scene is pure Go (no syscall/js, no cgo) so it builds for any architecture
// and is unit-tested natively. The wasm main only hands it a byte slice to
// fill plus mouse coordinates + clock-tick strings; all layout, hit-testing
// and RGBA painting live here.
package scene

import (
	"time"

	"github.com/go-iconoir/iconoir"
	"github.com/go-widgets/painter"
	"github.com/go-widgets/toolkit"
	"github.com/wasmdesk/wasmdock/internal/theme"
)

// App identifies one launchable application the iconbar offers. Id is the
// string sent to the compositor in a {type:"launch", app:Id} message; Glyph
// selects the built-in drawn icon (no external assets); Label is the short
// human-readable text painted to the right of the glyph inside the button.
type App struct {
	Id    string
	Glyph Glyph
	Label string
}

// Window identifies one open (or folded) compositor window. In the grouped-
// window dock model a window is not surfaced as its own button — it collapses
// into indicators on its launcher (see indicators.go / syncView). Id is the
// compositor's window id (echoed back in `focus` / `close` / `restore`
// messages, and used by the right-click window menu); Title is the window
// title (also used to map a window to its launcher when the compositor does
// not send an explicit App id); Minimized is true iff the compositor reports
// the window as currently folded; Focused is true iff this is the
// keyboard-focused window (its launcher gets the accent-filled active face).
// Role mirrors the compositor's role attribute — panels are filtered out
// server-side so Role is always "window" in practice, but the field is here
// so a future iconbar style for non-window roles needs no schema change.
type Window struct {
	Id        int    `json:"id"`
	Title     string `json:"title"`
	Minimized bool   `json:"minimized"`
	Focused   bool   `json:"focused"`
	Role      string `json:"role"`
	// Workspace mirrors the compositor's per-window workspace assignment
	// (1..WORKSPACE_COUNT). The compositor's windows_snapshot already
	// filters to the active workspace, so in v0 every entry sent over the
	// wire has Workspace == State.ActiveWorkspace — but the field is read by
	// the pager's occupancy dots and is here so a future "show all
	// workspaces" view needs no schema change.
	Workspace int `json:"workspace"`
	// App is the launcher id the window was spawned from (e.g. "terminal").
	// Forward-compatible: the compositor's windows_snapshot does not send it
	// yet, so it rides the payload the moment it does; until then the running
	// / focused-app indicators fall back to matching the window Title against
	// a launcher Label (see appIndexForWindow).
	App string `json:"app"`
}

// Glyph enumerates the built-in icon drawings.
type Glyph int

const (
	// GlyphTerminal draws the iconoir "terminal" mark (a command prompt).
	GlyphTerminal Glyph = iota
	// GlyphEditor draws the iconoir "page-edit" mark (a document being edited).
	GlyphEditor
	// GlyphFiles draws the iconoir "folder" mark.
	GlyphFiles
	// GlyphHello draws the iconoir "emoji" mark (a smiley) — the hello client's mark.
	GlyphHello
)

// Geometry constants, in surface pixels. The toolbar hugs the bottom of the
// surface; the surface itself is sized 1280 x BarHeight by the worker.
const (
	// BarHeight is the toolbar's vertical extent (and the surface height).
	BarHeight = 28
	// ClockW is the fixed pixel width of the clock accessory on the far
	// trailing (right) edge of the toolbar.
	ClockW = 80
	// IconGlyphPx is the side length of the icon drawn inside a launcher.
	IconGlyphPx = 16
)

// State is the toolbar's mutable model: surface size, the static launcher
// row, the active open-window row (one entry per non-panel window the
// compositor has open, including folded ones — flagged via Window.Minimized),
// the active workspace + workspace count (numeric model — the Workspace string
// derives from them), the current clock string, the cursor position (drives the
// hover magnification) and the active Openbox-compatible Theme.
//
// ActiveWorkspace defaults to 1 and WorkspaceCount to 4 (matches the
// compositor's WORKSPACE_COUNT constant). The compositor pushes a
// `workspace_changed` input event on every switch carrying both fields, so
// the dock can render the bar without ever holding a stale value.
type State struct {
	W, H            int
	Apps            []App
	Windows         []Window
	ActiveWorkspace int
	WorkspaceCount  int
	Workspace       string // legacy display-only label; SetWorkspace fills it
	Clock           string
	CursorX         int
	CursorY         int
	CursorInside    bool
	Theme           theme.Theme
	// Magnify configures the macOS-style hover magnification (see magnify.go).
	Magnify Magnify
	// badges holds the per-launcher attention-badge counts, keyed by app id
	// (see indicators.go). nil until the first SetBadge; read via BadgeCount.
	badges map[string]int

	// onWorkspace is the host callback fired (with a 1-based workspace index)
	// when the user picks a pager cell. Set by the wasm shell via
	// SetWorkspaceHandler; nil in unit tests.
	onWorkspace func(index int)
	// wsSyncing guards the pager's Current Observable while the model pushes
	// the authoritative ActiveWorkspace onto it, so a programmatic sync does
	// not echo back through onWorkspace as if the user had clicked.
	wsSyncing bool

	// view is the PERSISTENT toolkit widget tree (built once by ensureView,
	// cached here). Every frame syncView mutates its reactive state in place —
	// the clock text, the pager's Count / Occupied / Current, the dock's
	// running / active / badge indicators and cursor — instead of reallocating
	// the tree, so a hover repaint or a windows_changed event constructs no
	// widgets (the HARD "no per-frame rebuild" rule). nil until the first
	// Render / HitTest / geometry query.
	view *dockView
}

// dockView is the dock's persistent widget tree: the toolkit.DockPanel shell
// wrapping the AppDock iconbar with the WorkspacePager + Clock trailing
// accessories, plus the bar's themed background face and the 1-pixel top-border
// strip. Built once (ensureView); reactive state is mutated in place by syncView.
type dockView struct {
	panel  *toolkit.DockPanel
	dock   *toolkit.AppDock
	pager  *toolkit.WorkspacePager
	clock  *toolkit.Clock
	bar    *toolkit.Backdrop
	border *toolkit.Backdrop
	nApps  int // launcher count the dock's items were built for
}

// DefaultApps is the built-in launcher set the iconbar ships with.
func DefaultApps() []App {
	return []App{
		{Id: "terminal", Glyph: GlyphTerminal, Label: "Terminal"},
		{Id: "editor", Glyph: GlyphEditor, Label: "Editor"},
		{Id: "files", Glyph: GlyphFiles, Label: "Files"},
		{Id: "hello", Glyph: GlyphHello, Label: "Hello"},
	}
}

// New makes a toolbar State for a surface of width × height pixels carrying
// the default launcher set + the default active workspace (1 of 4) + an
// empty clock string (the worker posts a tick on boot to fill it in) + the
// default Fluxbox-light theme. The cursor is parked outside the surface.
func New(width, height int) *State {
	s := &State{
		W:               width,
		H:               height,
		Apps:            DefaultApps(),
		ActiveWorkspace: 1,
		WorkspaceCount:  4,
		Clock:           "",
		Theme:           theme.DefaultFluxboxLight(),
		Magnify:         DefaultMagnify(),
	}
	s.Workspace = workspaceLabel(s.ActiveWorkspace, s.WorkspaceCount)
	return s
}

// workspaceLabel formats the active/count pair as the text the legacy Workspace
// field carries. "1 of 4" is the chosen form: it reads like Fluxbox's
// "Workspace 1" but also surfaces the total count so the user knows how many
// slots cycle.
func workspaceLabel(active, count int) string {
	if count <= 0 {
		return itoa(active)
	}
	return itoa(active) + " of " + itoa(count)
}

// itoa is a tiny base-10 formatter for small non-negative ints. Keeping the
// dock's own formatter (instead of strconv.Itoa) keeps the wasm payload lean.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// SetCursor records the cursor position and whether it is over the surface.
func (s *State) SetCursor(x, y int, inside bool) {
	s.CursorX = x
	s.CursorY = y
	s.CursorInside = inside
}

// SetClock records the latest "HH:MM" clock string posted by the worker. The
// Clock widget reads it live through its Func seam, so no repaint copies the
// string into a widget field.
func (s *State) SetClock(t string) { s.Clock = t }

// SetTheme swaps in a new Openbox-compatible theme. The next Render call
// repaints the bar face + border with the new colours / gradients. Pure data;
// the caller (dock main.go) decides when to trigger a repaint.
func (s *State) SetTheme(th theme.Theme) { s.Theme = th }

// SetWorkspace records the active workspace label ("1", "2", ...). Kept as
// a legacy entry point for the `tick` event payload (worker.js may post
// a `workspace` field opportunistically). The numeric model is the
// authoritative source — SetActiveWorkspace / SetWorkspaceCount overwrite
// the label whenever they change so the two stay coherent.
func (s *State) SetWorkspace(w string) { s.Workspace = w }

// SetWorkspaceHandler registers the callback the dock fires (with a 1-based
// workspace index) when the user picks a pager cell. The wasm shell wires it to
// a `setWorkspace` message; unit tests leave it nil.
func (s *State) SetWorkspaceHandler(fn func(index int)) { s.onWorkspace = fn }

// SetActiveWorkspace records the active workspace number (1..WorkspaceCount)
// and refreshes the rendered Workspace label + the pager's highlighted cell.
// Clamped silently to the legal range so a malformed compositor payload cannot
// poison the model.
func (s *State) SetActiveWorkspace(n int) {
	if s.WorkspaceCount > 0 {
		if n < 1 {
			n = 1
		}
		if n > s.WorkspaceCount {
			n = s.WorkspaceCount
		}
	}
	s.ActiveWorkspace = n
	s.Workspace = workspaceLabel(s.ActiveWorkspace, s.WorkspaceCount)
	s.pushWorkspace()
}

// SetWorkspaceCount records the total workspace count (typically 4) and
// refreshes the rendered Workspace label. A non-positive count is treated
// as "unknown" and the label reduces to the active number only.
func (s *State) SetWorkspaceCount(n int) {
	if n < 0 {
		n = 0
	}
	s.WorkspaceCount = n
	if s.ActiveWorkspace < 1 {
		s.ActiveWorkspace = 1
	}
	if s.WorkspaceCount > 0 && s.ActiveWorkspace > s.WorkspaceCount {
		s.ActiveWorkspace = s.WorkspaceCount
	}
	s.Workspace = workspaceLabel(s.ActiveWorkspace, s.WorkspaceCount)
	s.pushWorkspace()
}

// pushWorkspace mirrors the authoritative ActiveWorkspace onto the pager's
// Current cell (0-based), guarded so this programmatic sync does not echo back
// through onWorkspace. A no-op until the view is built (New seeds the pager's
// initial cell in ensureView).
func (s *State) pushWorkspace() {
	if s.view == nil {
		return
	}
	s.wsSyncing = true
	s.view.pager.Current().Set(s.currentCell())
	s.wsSyncing = false
}

// currentCell is the pager's 0-based selected cell for the active workspace,
// clamped into [0, Count-1] (0 when the count is unknown).
func (s *State) currentCell() int {
	c := s.ActiveWorkspace - 1
	if c < 0 {
		c = 0
	}
	if s.WorkspaceCount > 0 && c > s.WorkspaceCount-1 {
		c = s.WorkspaceCount - 1
	}
	return c
}

// NextWorkspace returns the index the bar should cycle to on a forward
// step (scroll-wheel down over the pager). Wraps from WorkspaceCount back to 1.
// Returns the current active workspace if the count is non-positive so the dock
// cannot dispatch a bogus index.
func (s *State) NextWorkspace() int {
	if s.WorkspaceCount <= 0 {
		return s.ActiveWorkspace
	}
	n := s.ActiveWorkspace + 1
	if n > s.WorkspaceCount {
		n = 1
	}
	return n
}

// PrevWorkspace returns the index the bar should cycle to on a backward
// step (scroll-wheel up). Wraps from 1 back to WorkspaceCount.
func (s *State) PrevWorkspace() int {
	if s.WorkspaceCount <= 0 {
		return s.ActiveWorkspace
	}
	n := s.ActiveWorkspace - 1
	if n < 1 {
		n = s.WorkspaceCount
	}
	return n
}

// SetWindows replaces the open-window list (open + minimized, flagged via
// Window.Minimized). The slice is stored directly (callers must not mutate
// it after the call); the caller is the compositor's `windows_changed` event
// handler, which posts a fresh list on every change (new window, close,
// minimize, restore, focus shift, title rename).
func (s *State) SetWindows(ws []Window) { s.Windows = ws }

// ---- section geometry ----------------------------------------------------
//
// The layout is owned by the DockPanel, so these report the LAID-OUT bounds of
// the panel's widgets (after a sync) rather than fixed rectangles: the dock
// fills the leading span, the pager + clock sit at the trailing end.

// WorkspaceRect returns the workspace pager's laid-out rectangle.
func (s *State) WorkspaceRect() (x, y, w, h int) {
	s.syncView()
	b := s.view.pager.Bounds()
	return b.X, b.Y, b.W, b.H
}

// ClockRect returns the clock accessory's laid-out rectangle.
func (s *State) ClockRect() (x, y, w, h int) {
	s.syncView()
	b := s.view.clock.Bounds()
	return b.X, b.Y, b.W, b.H
}

// IconbarRect returns the AppDock (iconbar) laid-out rectangle — the span the
// DockPanel left between the leading edge and the trailing accessories.
func (s *State) IconbarRect() (x, y, w, h int) {
	s.syncView()
	b := s.view.dock.Bounds()
	return b.X, b.Y, b.W, b.H
}

// HitTestWorkspace reports whether (x, y) falls inside the workspace pager.
// Used by the dock to recognize a scroll-wheel event (cycle back/forward) or a
// left-click (switch to the clicked cell) over the workspace UI.
func (s *State) HitTestWorkspace(x, y int) bool {
	s.syncView()
	return s.view.pager.HitTest(x, y)
}

// ClickWorkspace routes a left-click at surface point (x, y) to the workspace
// pager, which switches to the clicked cell and — through the Current
// Observable the shell subscribed via SetWorkspaceHandler — fires the host's
// setWorkspace callback. A click that misses every cell is a no-op.
func (s *State) ClickWorkspace(x, y int) {
	s.syncView()
	b := s.view.pager.Bounds()
	s.view.pager.OnEvent(toolkit.Event{Kind: toolkit.EventClick, X: x - b.X, Y: y - b.Y})
}

// HitTest returns the launcher index under (x, y) in surface coordinates, or
// -1 if (x, y) does not fall inside any launcher icon. It defers to the
// composed toolkit.AppDock so paint and hit-testing read the SAME laid-out
// geometry (magnified under a hovering cursor, resting otherwise). Clicks on
// the pager or the clock fall outside the dock's bounds and return -1. Open
// windows collapse into indicators on their launcher, so per-window actions are
// reached through the right-click menu rather than a separate hit-test.
func (s *State) HitTest(x, y int) int {
	s.syncView()
	return s.view.dock.HitTest(x, y)
}

// ---- painting: the toolkit widget tree -----------------------------------

// dockToolkitTheme is the toolkit.Theme handed to the widget tree's Draw pass.
// The bar face paints from the richer Openbox theme.Theme stored on State
// (gradients + per-state title colours that toolkit.Theme cannot express); the
// AppDock, WorkspacePager and Clock render from this toolkit theme so they carry
// the consistent toolkit look.
var dockToolkitTheme = toolkit.DefaultLight()

// rgba converts a theme.Color (RGB triple) to an opaque toolkit.RGBA.
func rgba(c theme.Color) toolkit.RGBA { return toolkit.RGB(c[0], c[1], c[2]) }

// Render fills buf (a 4*W*H byte slice, RGBA32 row-major) with the toolbar
// at the current state. buf must be exactly the right size or Render panics
// (a size mismatch in the caller is a bug). The whole surface is opaque —
// the toolbar paints every pixel from edge to edge.
//
// Render syncs the PERSISTENT widget view to the current State (no tree
// rebuild), paints the bar's themed face, the DockPanel (dock + pager + clock),
// then overlays the 1-pixel top-border strip.
func Render(s *State, buf []byte) {
	need := 4 * s.W * s.H
	if len(buf) != need {
		panic("scene: buffer size mismatch")
	}
	p := painter.NewPixelPainter(buf, s.W, s.H)
	s.syncView()

	// The toolbar's Fluxbox face: a full-width themed Backdrop (gradient +
	// raised bevel) under the dock's widgets, so every pixel is opaque and the
	// bar recolours with the active Openbox theme.
	s.view.bar.Draw(p, dockToolkitTheme)

	// The dock shell: the AppDock iconbar plus the pager + clock accessories,
	// laid out and clipped by the DockPanel.
	s.view.panel.Draw(p, dockToolkitTheme)

	// Outer border on the very top edge of the toolbar (the bottom edge sits at
	// the bottom of the canvas, so a bottom border is not visible): a 1-pixel-tall
	// Backdrop filled with theme.Border.Color spanning the full surface width — a
	// toolkit widget in place of a per-pixel PutPixel loop.
	if s.Theme.Border.Width > 0 {
		s.view.border.SetBounds(toolkit.Rect{X: 0, Y: 0, W: s.W, H: 1})
		s.view.border.Draw(p, dockToolkitTheme)
	}
}

// ensureView builds the persistent widget tree the first time it is needed and
// caches it on the State: the DockPanel wrapping the AppDock iconbar with the
// WorkspacePager + Clock trailing accessories, plus the bar face + top-border
// Backdrops. The shell + accessories are built ONCE; the dock's AppDockItems are
// (re)built only when the launcher count changes (never in production — s.Apps
// is fixed after New), so no frame reallocates the tree. Each launcher's Icon
// painter closes over its stable Glyph; the reactive per-launcher flags are
// mutated in place by syncView. The pager's Current Observable is subscribed
// once so a user cell-click reaches the host through onWorkspace.
func (s *State) ensureView() {
	if s.view == nil {
		dock := toolkit.NewAppDock()
		pager := &toolkit.WorkspacePager{Count: s.WorkspaceCount}
		clock := toolkit.NewClock(time.Time{})
		clock.Align = toolkit.AlignCenter
		clock.Func = func(time.Time) string {
			if s.Clock == "" {
				return "--:--"
			}
			return s.Clock
		}
		panel := toolkit.NewDockPanel(dock)
		// Trailing[0] is the rightmost accessory: the clock hugs the right edge,
		// the pager sits just to its left.
		panel.Trailing = []toolkit.Widget{clock, pager}
		v := &dockView{
			panel:  panel,
			dock:   dock,
			pager:  pager,
			clock:  clock,
			bar:    &toolkit.Backdrop{},
			border: &toolkit.Backdrop{},
			nApps:  -1,
		}
		s.view = v
		// Seed the pager's cell from the model, then subscribe: a later change
		// is either a user click (onWorkspace fires) or a guarded model push
		// (wsSyncing swallows it).
		pager.Current().Set(s.currentCell())
		pager.Current().Subscribe(func(cur int) {
			if s.wsSyncing || s.onWorkspace == nil {
				return
			}
			s.onWorkspace(cur + 1)
		})
	}
	if s.view.nApps != len(s.Apps) {
		items := make([]toolkit.AppDockItem, len(s.Apps))
		for i, app := range s.Apps {
			g := app.Glyph
			items[i] = toolkit.AppDockItem{
				Id:    app.Id,
				Label: app.Label,
				Icon: func(p painter.Painter, r toolkit.Rect, ink toolkit.RGBA) {
					drawGlyph(p, g, r, ink)
				},
			}
		}
		s.view.dock.Items = items
		s.view.nApps = len(s.Apps)
	}
}

// syncView binds the live State onto the persistent widget tree without
// rebuilding it: the bar face + border colour track the active theme, the pager
// tracks the workspace count / occupancy / current cell, the clock reads its
// string through its Func, and the AppDock's per-launcher running / active /
// badge indicators, magnification knobs and cursor are set through the widget's
// own fields / setters. It then lays the DockPanel out across the surface (its
// own layout pass positions the dock + accessories) so a subsequent Draw /
// HitTest / ItemRects reads the current geometry.
func (s *State) syncView() {
	s.ensureView()
	v := s.view

	// Bar face + border from the active Openbox theme.
	barBackdrop(v.bar, s.Theme, toolkit.Rect{X: 0, Y: 0, W: s.W, H: s.H})
	v.border.Fill = rgba(s.Theme.Border.Color)

	// Workspace pager: count, occupancy dots and width track the model; the
	// current cell is pushed authoritatively (guarded) so a compositor switch
	// moves the highlight without echoing back as a user click.
	v.pager.Count = s.WorkspaceCount
	v.pager.Occupied = s.workspaceOccupancy()
	v.pager.SetBounds(toolkit.Rect{W: pagerWidth(s.WorkspaceCount)})
	s.pushWorkspace()

	// Clock: fixed width; its text is read live through clock.Func, so nothing
	// to copy here. Only the width matters to the DockPanel layout.
	v.clock.SetBounds(toolkit.Rect{W: ClockW})

	// Dock indicators: mutate the persistent items in place (grouped-window
	// model — Running lights the "app open" dot, Active fills the focused
	// launcher, Badge overlays the attention count).
	running := s.launcherRunning()
	focus := s.focusedLauncher()
	for i := range v.dock.Items {
		v.dock.Items[i].Running = running[i]
		v.dock.Items[i].Active = i == focus
		v.dock.Items[i].Badge = s.BadgeCount(s.Apps[i].Id)
	}
	v.dock.Magnify = s.Magnify.On
	v.dock.MaxScale = s.Magnify.MaxScale
	v.dock.Radius = s.Magnify.Radius
	v.dock.SetCursor(s.CursorX, s.CursorInside)

	// Lay the panel out across the whole surface; DockPanel positions the dock
	// (the span before the accessories) and the trailing pager + clock.
	v.panel.SetBounds(toolkit.Rect{X: 0, Y: 0, W: s.W, H: s.H})
}

// pagerWidth is the pixel width a WorkspacePager needs for count cells at the
// toolkit's compact cell metrics (device pixels at metric scale 1, matching the
// dock's fixed-pixel geometry). Zero when the count is non-positive.
func pagerWidth(count int) int {
	if count <= 0 {
		return 0
	}
	return count*toolkit.WorkspacePagerCellW + (count-1)*toolkit.WorkspacePagerGap
}

// barBackdrop configures bd as the toolbar's themed face: the inactive-title
// background colour under a raised Fluxbox bevel, with the theme's gradient when
// it asks for one. Reusing one persistent Backdrop (mutated here) keeps the
// per-frame paint allocation-free.
func barBackdrop(bd *toolkit.Backdrop, th theme.Theme, r toolkit.Rect) {
	bg := th.Window.Inactive.Title.Bg
	bd.Fill = rgba(bg.Color)
	bd.Bevel = toolkit.BevelRaised
	bd.GradientTo = toolkit.RGBA{}
	bd.GradientDir = toolkit.GradientVertical
	if bg.Gradient != theme.GradientFlat {
		bd.GradientTo = rgba(bg.ColorTo)
		bd.GradientDir = gradientDir(bg.Gradient)
	}
	bd.SetBounds(r)
}

// gradientDir maps a theme.GradientType onto the toolkit.Backdrop gradient
// direction. Flat / unknown fall back to GradientVertical, though a Flat bg
// never reaches here (barBackdrop gates the gradient on != GradientFlat).
func gradientDir(g theme.GradientType) toolkit.GradientDir {
	switch g {
	case theme.GradientHorizontal:
		return toolkit.GradientHorizontal
	case theme.GradientDiagonal:
		return toolkit.GradientDiagonal
	case theme.GradientCrossDiagonal:
		return toolkit.GradientCrossDiagonal
	default:
		return toolkit.GradientVertical
	}
}

// ---- glyph drawing -------------------------------------------------------

// glyphStem maps a Glyph onto its iconoir stem (verified against
// iconoir.Names()): the launcher marks are drawn from the iconoir icon family —
// no hand-drawn pixel art — so the dock reads the same icon set as the rest of
// the toolkit UI. An unknown glyph maps to "" (no stem) and falls back to a
// solid square in drawGlyph.
func glyphStem(g Glyph) string {
	switch g {
	case GlyphTerminal:
		return "terminal"
	case GlyphEditor:
		return "page-edit"
	case GlyphFiles:
		return "folder"
	case GlyphHello:
		return "emoji"
	default:
		return ""
	}
}

// drawGlyph paints one launcher's icon mark into r via iconoir.Draw. A
// non-positive rect is a no-op; an unknown glyph (no stem, or a stem iconoir
// does not carry) paints a solid square so the slot stays visible.
func drawGlyph(p painter.Painter, g Glyph, r toolkit.Rect, ink toolkit.RGBA) {
	if r.W <= 0 || r.H <= 0 {
		return
	}
	if stem := glyphStem(g); stem != "" && iconoir.Draw(p, r, stem, ink) {
		return
	}
	//bricolint:allow icon-paint leaf: last-resort solid square for a glyph iconoir does not carry, so the AppDockItem.Icon slot stays visible (primary path is iconoir.Draw above); the toolkit delegates icon painting to this func(painter.Painter,…) callback.
	p.FillRect(r, ink)
}
