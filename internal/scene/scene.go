// SPDX-License-Identifier: BSD-3-Clause
//
// Package scene paints the wasmdock surface as a Fluxbox-style bottom
// toolbar: a full-width, 28-pixel-tall bevelled gray bar split into three
// sections that read left-to-right —
//
//   - a fixed-width workspace section on the left rendering the active
//     workspace as "<active> of <count>" (default "1 of 4"). Left-clicking
//     the section cycles to the next workspace; scroll-wheel up/down
//     cycles backward/forward. The active workspace is reported by the
//     compositor through the `workspace_changed` input event (kind:
//     "workspace_changed", payload {active:int, count:int});
//   - an iconbar in the middle: a toolkit.AppDock (the modern, grouped-window
//     dock) with one icon item per known LAUNCHER (terminal / editor / files /
//     hello). Open windows do NOT get their own buttons — they collapse into
//     indicators on their launcher: a "running" dot when the app has at least
//     one open window, an accent-filled face for the launcher whose window
//     holds focus, and an attention badge for the count. Left-clicking a
//     launcher posts a `launch` message; per-window actions (focus / close)
//     are reached through the right-click application menu;
//   - a fixed-width clock ("HH:MM") on the right, kept in sync by a `tick`
//     event posted by the JS worker every 30 seconds.
//
// # Toolkit widget model
//
// The bar is composed as a go-widgets/toolkit widget tree rather than a
// sequence of hand-drawn rects: the shell is a toolkit.HBox with two
// fixed-width ends (the workspace label + the clock) and a flex iconbar in
// the middle, exactly mirroring the three-section geometry. The workspace /
// clock ends are `section` leaf widgets; the iconbar is a thin container that
// composes a toolkit.AppDock (see buildDock) — the widget owns all the dock
// chrome (rounded item faces, running dots, active fill, attention badges) and
// the hover magnification, so there are no hand-drawn Fluxbox button bevels
// left. Each launcher hands the dock an icon painter; the app glyphs that are
// bespoke Fluxbox marks (terminal / hello) stay custom Draw while the two that
// map cleanly onto stock artwork reuse the toolkit icon library (editor →
// DrawIconNew, files → DrawIconOpen). The workspace / clock ends compose their
// Fluxbox bevel / gradient chrome from a toolkit.Backdrop (gradient fill +
// raised bevel), so no hand-drawn shape-painting is left in the toolbar.
//
// The State value remains the single source of truth for layout: the pure
// *Rect / HitTest* geometry methods are unchanged, so the wasm main's
// button/wheel-aware event dispatch keeps working byte-for-byte. Render
// rebuilds the (cheap) widget view from the current State each frame, so a
// direct field mutation is always reflected.
//
// scene is pure Go (no syscall/js, no cgo) so it builds for any architecture
// and is unit-tested natively. The wasm main only hands it a byte slice to
// fill plus mouse coordinates + clock-tick strings; all layout, hit-testing
// and RGBA painting live here.
package scene

import (
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
// into indicators on its launcher (see indicators.go / buildDock). Id is the
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
	// wire has Workspace == State.ActiveWorkspace — but the field is here
	// so a future "show all workspaces" view (e.g. a pager) needs no
	// schema change.
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
	// GlyphTerminal draws a command prompt: ">" caret + underscore cursor.
	GlyphTerminal Glyph = iota
	// GlyphEditor draws a document with a folded corner (toolkit stock New icon).
	GlyphEditor
	// GlyphFiles draws a folder shape (toolkit stock Open icon).
	GlyphFiles
	// GlyphHello draws a smile arc — the hello stub client's mark.
	GlyphHello
)

// Geometry constants, in surface pixels. The toolbar hugs the bottom of the
// surface; the surface itself is sized 1280 x BarHeight by the worker.
const (
	// BarHeight is the toolbar's vertical extent (and the surface height).
	BarHeight = 28
	// WorkspaceW is the fixed pixel width of the workspace section on the
	// left edge of the toolbar.
	WorkspaceW = 100
	// ClockW is the fixed pixel width of the clock section on the right
	// edge of the toolbar.
	ClockW = 80
	// IconbarButtonW is the resting pixel width of one iconbar button.
	IconbarButtonW = 120
	// IconbarButtonH is the inner height of an iconbar button (the toolbar
	// reserves 2px of vertical breathing room above + below).
	IconbarButtonH = 24
	// IconbarButtonGap is the horizontal spacing between adjacent buttons.
	IconbarButtonGap = 2
	// IconbarVPad is the vertical padding between the toolbar top/bottom
	// and the iconbar button row.
	IconbarVPad = 2
	// IconGlyphPx is the side length of the icon drawn inside a button.
	IconGlyphPx = 16
	// IconGlyphLeftPad is the gap between the button's left bevel and the
	// glyph.
	IconGlyphLeftPad = 4
	// IconLabelGap is the gap between the glyph and the start of the label
	// text.
	IconLabelGap = 4
	// SeparatorW is the horizontal width reserved between the static launcher
	// row and the dynamic open-window row. The separator is painted as a
	// 1-pixel dark line centered inside this gap so the user reads the two
	// sub-sections as distinct stripes.
	SeparatorW = 8
)

// State is the toolbar's mutable model: surface size, the static launcher
// row, the active open-window row (one button per non-panel window the
// compositor has open, including folded ones — flagged via Window.Minimized),
// the active workspace + workspace count (numeric model — Workspace string
// derives from them in Render), the current clock string, the cursor
// position (recorded for a future hover highlight; unused by the v0 paint
// pass) and the active Openbox-compatible Theme.
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

// workspaceLabel formats the active/count pair as the text the bar renders.
// "1 of 4" is the chosen form: it reads like Fluxbox's "Workspace 1" but
// also surfaces the total count so the user knows how many slots cycle.
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

// SetClock records the latest "HH:MM" clock string posted by the worker.
func (s *State) SetClock(t string) { s.Clock = t }

// SetTheme swaps in a new Openbox-compatible theme. The next Render call
// repaints every section with the new colours / gradients. Pure data; the
// caller (dock main.go) decides when to trigger a repaint.
func (s *State) SetTheme(th theme.Theme) { s.Theme = th }

// SetWorkspace records the active workspace label ("1", "2", ...). Kept as
// a legacy entry point for the `tick` event payload (worker.js may post
// a `workspace` field opportunistically). The numeric model is the
// authoritative source — SetActiveWorkspace / SetWorkspaceCount overwrite
// the label whenever they change so the two stay coherent.
func (s *State) SetWorkspace(w string) { s.Workspace = w }

// SetActiveWorkspace records the active workspace number (1..WorkspaceCount)
// and refreshes the rendered Workspace label. Clamped silently to the legal
// range so a malformed compositor payload cannot poison the model.
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
}

// NextWorkspace returns the index the bar should cycle to on a forward
// step (left-click on the workspace section, scroll-wheel down). Wraps
// from WorkspaceCount back to 1. Returns the current active workspace if
// the count is non-positive so the dock cannot dispatch a bogus index.
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

// WorkspaceRect returns the workspace section rectangle.
func (s *State) WorkspaceRect() (x, y, w, h int) {
	return 0, 0, WorkspaceW, s.H
}

// ClockRect returns the clock section rectangle.
func (s *State) ClockRect() (x, y, w, h int) {
	return s.W - ClockW, 0, ClockW, s.H
}

// IconbarRect returns the iconbar (middle) section rectangle, expanding to
// fill the gap between the workspace label and the clock.
func (s *State) IconbarRect() (x, y, w, h int) {
	x = WorkspaceW
	w = s.W - WorkspaceW - ClockW
	if w < 0 {
		w = 0
	}
	return x, 0, w, s.H
}

// HitTestWorkspace reports whether (x, y) falls inside the workspace section
// on the left edge of the toolbar. Used by the dock to recognize a
// left-click (cycle to next workspace) or scroll-wheel event (cycle
// back/forward) over the workspace UI.
func (s *State) HitTestWorkspace(x, y int) bool {
	wx, wy, ww, wh := s.WorkspaceRect()
	return x >= wx && x < wx+ww && y >= wy && y < wy+wh
}

// HitTest returns the launcher index under (x, y) in surface coordinates, or
// -1 if (x, y) does not fall inside any launcher icon. It defers to the
// composed toolkit.AppDock so paint and hit-testing read the SAME laid-out
// geometry (magnified under a hovering cursor, resting otherwise). Clicks on
// the workspace label or the clock fall outside the dock's bounds and return
// -1. Open windows collapse into indicators on their launcher, so per-window
// actions are reached through the right-click menu rather than a separate
// hit-test.
func (s *State) HitTest(x, y int) int {
	d := s.buildDock()
	return d.HitTest(x, y)
}

// ---- painting: the toolkit widget tree -----------------------------------

// dockToolkitTheme is the toolkit.Theme handed to the widget tree's Draw
// pass. The dock's leaves paint from the richer Openbox theme.Theme stored on
// State (gradients + per-state title colours that toolkit.Theme cannot
// express), so this value is never consulted for colour — it exists only to
// satisfy the Widget.Draw(painter, *toolkit.Theme) signature.
var dockToolkitTheme = toolkit.DefaultLight()

// rgba converts a theme.Color (RGB triple) to an opaque toolkit.RGBA.
func rgba(c theme.Color) toolkit.RGBA { return toolkit.RGB(c[0], c[1], c[2]) }

// Render fills buf (a 4*W*H byte slice, RGBA32 row-major) with the toolbar
// at the current state. buf must be exactly the right size or Render panics
// (a size mismatch in the caller is a bug). The whole surface is opaque —
// the toolbar paints every pixel from edge to edge.
//
// Render builds the widget view from the current State, lays it out with one
// container pass and paints it, then overlays the 1-pixel top border chrome.
func Render(s *State, buf []byte) {
	need := 4 * s.W * s.H
	if len(buf) != need {
		panic("scene: buffer size mismatch")
	}
	p := painter.NewPixelPainter(buf, s.W, s.H)
	root := buildRoot(s)
	root.SetBounds(toolkit.Rect{X: 0, Y: 0, W: s.W, H: s.H})
	root.Draw(p, dockToolkitTheme)

	// Outer border on the very top edge of the toolbar (the bottom edge sits
	// at the bottom of the canvas, so a bottom border is not visible). One
	// pixel of theme.Border.Color spanning the full surface width.
	if s.Theme.Border.Width > 0 {
		bc := rgba(s.Theme.Border.Color)
		for x := 0; x < s.W; x++ {
			p.PutPixel(x, 0, bc)
		}
	}
}

// buildRoot assembles the dock's widget tree for the current State: an HBox
// shell with a fixed workspace label on the left, a flex iconbar in the
// middle, and a fixed clock on the right. It is cheap enough to rebuild every
// frame, which keeps State the single source of truth (a direct field
// mutation is reflected on the next Render without a separate sync step).
func buildRoot(s *State) *toolkit.HBox {
	root := toolkit.NewHBox()
	root.Spacing = 0

	ws := &section{
		bg:   s.Theme.Window.Inactive.Title.Bg,
		text: s.Workspace,
		ink:  s.Theme.Window.Inactive.Title.Label.Color,
	}
	root.AddFixed(ws, WorkspaceW)

	ib := &iconbar{s: s}
	root.AddFlex(ib, 1)

	clock := s.Clock
	if clock == "" {
		clock = "--:--"
	}
	ck := &section{
		bg:   s.Theme.Osd.Bg,
		text: clock,
		ink:  s.Theme.Osd.Label.Color,
	}
	root.AddFixed(ck, ClockW)
	return root
}

// section is a fixed-width bevelled toolbar end (the workspace label + the
// clock): a gradient background, a raised bevel, and one line of centred
// text. Both ends share this leaf; only their bg / text / ink differ.
type section struct {
	toolkit.Base
	bg   theme.Bg
	text string
	ink  theme.Color
}

// Draw paints the section as a composed toolkit.Backdrop — a gradient (or flat)
// face under a raised Fluxbox bevel — then overlays the centred label. The
// Backdrop owns the last hand-drawn Fluxbox chrome the toolbar ends used to
// paint by hand (the per-pixel gradient + the 1-pixel raised bevel), so there
// is no bespoke shape-drawing left here; only the toolkit text stack.
func (w *section) Draw(p painter.Painter, _ *toolkit.Theme) {
	r := w.Bounds()
	bd := toolkit.Backdrop{
		Fill:  rgba(w.bg.Color),
		Bevel: toolkit.BevelRaised,
	}
	if w.bg.Gradient != theme.GradientFlat {
		bd.GradientTo = rgba(w.bg.ColorTo)
		bd.GradientDir = gradientDir(w.bg.Gradient)
	}
	bd.SetBounds(r)
	bd.Draw(p, dockToolkitTheme)
	tx := r.X + (r.W-toolkit.TextWidth(w.text))/2
	ty := r.Y + (r.H-toolkit.GlyphHeight())/2
	toolkit.DrawText(p, tx, ty, w.text, rgba(w.ink))
}

// gradientDir maps a theme.GradientType onto the toolkit.Backdrop gradient
// direction. Flat / unknown fall back to GradientVertical, though a Flat bg
// never reaches here (section.Draw gates the gradient on != GradientFlat).
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

// iconbar is the flex middle section: it paints the active-title background +
// bevel over the iconbar rect, then draws its launcher buttons, the launcher/
// window separator, and its window buttons — clipping + stopping exactly like
// the surface geometry dictates so a narrow surface degrades gracefully.
type iconbar struct {
	toolkit.Base
	s *State
}

// Draw composes the iconbar from a toolkit.AppDock (the modern grouped-window
// dock) and paints it. buildDock maps each launcher to a dock icon item and
// folds the running / active / badge state onto it, so the widget owns all the
// chrome (rounded faces, running dots, active fill, attention badges) and the
// hover magnification — no hand-drawn Fluxbox bevels here.
func (ib *iconbar) Draw(p painter.Painter, th *toolkit.Theme) {
	d := ib.s.buildDock()
	d.Draw(p, th)
}

// buildDock assembles the toolkit.AppDock for the current State: one
// AppDockItem per LAUNCHER (s.Apps). Open windows do NOT get their own button —
// they collapse into indicators on their launcher: item.Running lights the "app
// is open" dot, item.Active fills the launcher whose window holds focus, and
// item.Badge overlays the attention count. The dock is bounded to the iconbar
// rect and handed the live cursor so its magnification + hit-testing read the
// same layout the user sees. Cheap enough to rebuild every frame, keeping State
// the single source of truth.
func (s *State) buildDock() *toolkit.AppDock {
	running := s.launcherRunning()
	focus := s.focusedLauncher()
	items := make([]toolkit.AppDockItem, len(s.Apps))
	for i, app := range s.Apps {
		g := app.Glyph
		items[i] = toolkit.AppDockItem{
			Id:    app.Id,
			Label: app.Label,
			Icon: func(p painter.Painter, r toolkit.Rect, ink toolkit.RGBA) {
				drawGlyph(p, g, r, ink)
			},
			Running: running[i],
			Active:  i == focus,
			Badge:   s.BadgeCount(app.Id),
		}
	}
	d := toolkit.NewAppDock(items...)
	d.Magnify = s.Magnify.On
	d.MaxScale = s.Magnify.MaxScale
	d.Radius = s.Magnify.Radius
	d.SetCursor(s.CursorX, s.CursorInside)
	ix, _, iw, _ := s.IconbarRect()
	d.SetBounds(toolkit.Rect{X: ix, Y: 0, W: iw, H: s.H})
	return d
}

// ---- glyph drawing -------------------------------------------------------

// drawGlyph paints one of the built-in icon marks into r. The two glyphs that
// map cleanly onto the toolkit's stock icon library reuse it (editor →
// DrawIconNew, files → DrawIconOpen); the bespoke Fluxbox marks (terminal /
// hello) stay custom Draw.
func drawGlyph(p painter.Painter, g Glyph, r toolkit.Rect, ink toolkit.RGBA) {
	if r.W <= 0 || r.H <= 0 {
		return
	}
	switch g {
	case GlyphTerminal:
		drawGlyphTerminal(p, r, ink)
	case GlyphEditor:
		toolkit.DrawIconNew(p, r, ink)
	case GlyphFiles:
		toolkit.DrawIconOpen(p, r, ink)
	case GlyphHello:
		drawGlyphHello(p, r, ink)
	default:
		// Unknown glyph: paint a solid square so the slot is still visible.
		p.FillRect(r, ink)
	}
}

func drawGlyphTerminal(p painter.Painter, r toolkit.Rect, ink toolkit.RGBA) {
	x, y, w, h := r.X, r.Y, r.W, r.H
	// ">" caret + underscore cursor inside the box.
	cx := x + w*2/5
	cy := y + h/2
	arm := h / 4
	for t := 0; t <= arm; t++ {
		p.PutPixel(cx-arm+t, cy-arm+t, ink)
		p.PutPixel(cx-arm+t, cy+arm-t, ink)
	}
	uy := y + h*3/4
	for ux := x + 2; ux < x+w-2; ux++ {
		p.PutPixel(ux, uy, ink)
	}
}

func drawGlyphHello(p painter.Painter, r toolkit.Rect, ink toolkit.RGBA) {
	x, y, w, h := r.X, r.Y, r.W, r.H
	// Smile arc: bottom half of a "circle" inside the box.
	cx := x + w/2
	cy := y + h/2
	rad := w / 2
	if h/2 < rad {
		rad = h / 2
	}
	for i := -rad; i <= rad; i++ {
		p.PutPixel(cx+i, cy+(rad-abs(i))/2, ink)
	}
	// Two eyes.
	p.PutPixel(cx-rad/2, cy-rad/2, ink)
	p.PutPixel(cx+rad/2, cy-rad/2, ink)
}

func abs(i int) int {
	if i < 0 {
		return -i
	}
	return i
}
