// SPDX-License-Identifier: BSD-3-Clause

package scene

import (
	"testing"

	"github.com/go-widgets/painter"
	"github.com/go-widgets/toolkit"
	"github.com/wasmdesk/wasmdock/internal/theme"
)

// Surface size used by most tests: 1280-wide x 28-tall, matching the worker
// surface dimensions.
const (
	tW = 1280
	tH = BarHeight
)

func newBuf(s *State) []byte { return make([]byte, 4*s.W*s.H) }

// newPainter returns a zeroed RGBA buffer of w*h plus a PixelPainter over it,
// for tests that drive the low-level Fluxbox chrome helpers directly.
func newPainter(w, h int) ([]byte, *painter.PixelPainter) {
	buf := make([]byte, 4*w*h)
	return buf, painter.NewPixelPainter(buf, w, h)
}

// glyphInk is the near-black ink the dock passes its icon painters; the glyph
// tests use it directly now that drawGlyph takes the ink from its host.
var glyphInk = toolkit.RGB(0x1a, 0x1a, 0x1a)

func TestNewHasDefaults(t *testing.T) {
	s := New(tW, tH)
	if got, want := len(s.Apps), 4; got != want {
		t.Fatalf("default apps = %d, want %d", got, want)
	}
	want := []string{"terminal", "editor", "files", "hello"}
	for i, a := range s.Apps {
		if a.Id != want[i] {
			t.Fatalf("app[%d].Id = %q, want %q", i, a.Id, want[i])
		}
	}
	if s.Workspace != "1 of 4" {
		t.Fatalf("default workspace = %q, want %q", s.Workspace, "1 of 4")
	}
	if s.Clock != "" {
		t.Fatalf("default clock = %q, want empty (worker will tick)", s.Clock)
	}
	if s.Theme.Border.Width != 1 {
		t.Fatalf("default theme missing border width")
	}
}

// SectionLayout — the workspace label ends at x=WorkspaceW, the clock begins
// at x=W-ClockW, and the iconbar fills the middle.
func TestSectionLayout(t *testing.T) {
	s := New(tW, tH)
	wx, _, ww, wh := s.WorkspaceRect()
	if wx != 0 || ww != WorkspaceW || wh != tH {
		t.Fatalf("workspace rect = (%d,_,%d,%d), want (0,_,%d,%d)", wx, ww, wh, WorkspaceW, tH)
	}
	cx, _, cw, _ := s.ClockRect()
	if cx != tW-ClockW || cw != ClockW {
		t.Fatalf("clock rect = (%d,_,%d,_), want (%d,_,%d,_)", cx, cw, tW-ClockW, ClockW)
	}
	ix, _, iw, _ := s.IconbarRect()
	if ix != WorkspaceW || iw != tW-WorkspaceW-ClockW {
		t.Fatalf("iconbar rect = (%d,_,%d,_), want (%d,_,%d,_)", ix, iw, WorkspaceW, tW-WorkspaceW-ClockW)
	}
}

// On a narrow surface where workspace + clock would overlap, the iconbar
// collapses to width 0 (never negative).
func TestIconbarClampsToZeroOnNarrowSurface(t *testing.T) {
	s := New(50, tH) // 50 < WorkspaceW (100) + ClockW (80)
	_, _, iw, _ := s.IconbarRect()
	if iw != 0 {
		t.Fatalf("iconbar width on narrow surface = %d, want 0", iw)
	}
}

// The iconbar is composed from a toolkit.AppDock: LauncherRects reports one
// laid-out rect per launcher, and a click at a launcher's centre HitTests to
// that launcher index, whose Apps[i].Id is the documented launch string. This
// is the dock's paint == hit-test contract, exercised through a full Render.
func TestDockLauncherHitTest(t *testing.T) {
	wantIDs := []string{"terminal", "editor", "files", "hello"}
	s := New(tW, tH)
	buf := newBuf(s)
	Render(s, buf) // renders through the AppDock
	rects := s.LauncherRects()
	if len(rects) != len(s.Apps) {
		t.Fatalf("LauncherRects len %d, want %d", len(rects), len(s.Apps))
	}
	for i, r := range rects {
		if r[2] <= 0 || r[3] <= 0 {
			t.Fatalf("launcher %d has empty rect %v", i, r)
		}
		cx := r[0] + r[2]/2
		cy := r[1] + r[3]/2
		if got := s.HitTest(cx, cy); got != i {
			t.Fatalf("HitTest centre of launcher %d = %d, want %d", i, got, i)
		}
		if got := s.Apps[i].Id; got != wantIDs[i] {
			t.Fatalf("launcher %d dispatches %q, want %q", i, got, wantIDs[i])
		}
	}
}

// The widget tree is PERSISTENT: Render / HitTest / LauncherRects across many
// frames (including cursor moves and a windows_changed) reuse the SAME dockView
// and AppDock rather than rebuilding, and the reactive indicators are mutated in
// place on that one dock. This is the "no per-frame rebuild" contract.
func TestPersistentViewReusedAcrossFrames(t *testing.T) {
	s := New(tW, tH)
	buf := newBuf(s)
	Render(s, buf)
	view0 := s.view
	dock0 := s.view.dock
	if view0 == nil || dock0 == nil {
		t.Fatalf("view not built after first Render")
	}
	// A hover repaint, a hit-test, a geometry probe and a windows change must all
	// reuse the same widgets.
	s.SetCursor(200, tH/2, true)
	Render(s, buf)
	s.HitTest(200, tH/2)
	s.LauncherRects()
	s.SetWindows([]Window{{Id: 1, Title: "Terminal", Focused: true}})
	s.SetBadge("terminal", 2)
	Render(s, buf)
	if s.view != view0 {
		t.Fatalf("dockView was rebuilt across frames (persistent-tree violation)")
	}
	if s.view.dock != dock0 {
		t.Fatalf("AppDock was rebuilt across frames (persistent-tree violation)")
	}
	// The running/active/badge indicators were folded onto the SAME dock in place.
	it := s.view.dock.Items[0] // terminal
	if !it.Running || !it.Active || it.Badge != 2 {
		t.Fatalf("terminal item indicators not synced in place: %+v", it)
	}
}

// A change in the launcher count (never in production, but a public field) is the
// one event that re-materialises the dock's items; the shell + section leaves
// stay the same persistent widgets.
func TestAppCountChangeRebuildsItemsOnly(t *testing.T) {
	s := New(tW, tH)
	buf := newBuf(s)
	Render(s, buf)
	view0, ws0 := s.view, s.view.ws
	s.Apps = []App{{Id: "only", Glyph: GlyphTerminal, Label: "Only"}}
	Render(s, buf)
	if s.view != view0 || s.view.ws != ws0 {
		t.Fatalf("shell/section leaves were rebuilt on an app-count change")
	}
	if len(s.view.dock.Items) != 1 || s.view.dock.Items[0].Id != "only" {
		t.Fatalf("dock items not rematerialised for the new app set: %+v", s.view.dock.Items)
	}
}

// Clicks on the workspace label / clock fall outside the dock bounds so
// HitTest returns -1.
func TestClicksOnWorkspaceAndClockAreInert(t *testing.T) {
	s := New(tW, tH)
	if got := s.HitTest(WorkspaceW/2, tH/2); got != -1 {
		t.Fatalf("workspace click HitTest = %d, want -1", got)
	}
	if got := s.HitTest(tW-ClockW/2, tH/2); got != -1 {
		t.Fatalf("clock click HitTest = %d, want -1", got)
	}
}

// Render fills the whole surface (no transparent pixels) and paints the
// workspace + iconbar + clock in their expected sections.
func TestRenderFillsAllPixelsOpaque(t *testing.T) {
	s := New(tW, tH)
	s.SetClock("12:34")
	buf := newBuf(s)
	Render(s, buf)
	for i := 3; i < len(buf); i += 4 {
		if buf[i] != 0xFF {
			t.Fatalf("non-opaque pixel at byte %d: alpha=%d", i, buf[i])
		}
	}
}

func TestRenderPanicsOnSizeMismatch(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on size mismatch")
		}
	}()
	s := New(16, BarHeight)
	Render(s, make([]byte, 4))
}

// The workspace section should show ink different from its background at
// the painted-glyph rows.
func TestRenderWorkspaceLabelInked(t *testing.T) {
	s := New(tW, tH)
	buf := newBuf(s)
	Render(s, buf)
	found := false
	for y := 0; y < tH && !found; y++ {
		for x := 0; x < WorkspaceW && !found; x++ {
			off := (y*tW + x) * 4
			if buf[off] < 0x40 && buf[off+1] < 0x40 && buf[off+2] < 0x40 {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("workspace label glyph never inked")
	}
}

// With an explicit clock string the clock section paints near-black ink
// somewhere inside it.
func TestRenderClockInked(t *testing.T) {
	s := New(tW, tH)
	s.SetClock("09:42")
	buf := newBuf(s)
	Render(s, buf)
	cx, _, cw, _ := s.ClockRect()
	found := false
	for y := 0; y < tH && !found; y++ {
		for x := cx; x < cx+cw && !found; x++ {
			off := (y*tW + x) * 4
			if buf[off] < 0x40 && buf[off+1] < 0x40 && buf[off+2] < 0x40 {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("clock glyph never inked")
	}
}

// An empty clock falls back to the placeholder "--:--" so the section is
// always visually present.
func TestRenderClockFallback(t *testing.T) {
	s := New(tW, tH)
	s.Clock = ""
	buf := newBuf(s)
	Render(s, buf)
	cx, _, cw, _ := s.ClockRect()
	inked := 0
	for y := 0; y < tH; y++ {
		for x := cx; x < cx+cw; x++ {
			off := (y*tW + x) * 4
			if buf[off] < 0x40 && buf[off+1] < 0x40 && buf[off+2] < 0x40 {
				inked++
			}
		}
	}
	if inked == 0 {
		t.Fatalf("fallback clock '--:--' never inked")
	}
}

// The top border row must be the theme.Border.Color across the full width.
func TestRenderTopBorderColor(t *testing.T) {
	s := New(tW, tH)
	buf := newBuf(s)
	Render(s, buf)
	bc := s.Theme.Border.Color
	for x := 0; x < tW; x++ {
		off := (0*tW + x) * 4
		if buf[off] != bc[0] || buf[off+1] != bc[1] || buf[off+2] != bc[2] {
			t.Fatalf("top border at x=%d = %v, want %v", x, buf[off:off+3], bc)
		}
	}
}

// Disabling the border (Width = 0) skips the top stroke.
func TestRenderTopBorderSkippedWhenWidthZero(t *testing.T) {
	s := New(tW, tH)
	s.Theme.Border.Width = 0
	buf := newBuf(s)
	Render(s, buf)
	off := 0
	bc := s.Theme.Border.Color
	if buf[off] == bc[0] && buf[off+1] == bc[1] && buf[off+2] == bc[2] {
		t.Fatalf("top border still painted when Width=0")
	}
}

// SetCursor / SetWorkspace / SetClock store their arguments.
func TestSetters(t *testing.T) {
	s := New(tW, tH)
	s.SetCursor(11, 22, true)
	if s.CursorX != 11 || s.CursorY != 22 || !s.CursorInside {
		t.Fatalf("SetCursor not stored: %+v", s)
	}
	s.SetWorkspace("3")
	if s.Workspace != "3" {
		t.Fatalf("SetWorkspace not stored: %q", s.Workspace)
	}
	s.SetClock("23:59")
	if s.Clock != "23:59" {
		t.Fatalf("SetClock not stored: %q", s.Clock)
	}
}

// ---- Fluxbox chrome helpers (painter-level) ------------------------------

// Each glyph + the default branch (unknown glyph) must paint at least one
// pixel of ink inside its tile.
func TestEachGlyphPaints(t *testing.T) {
	glyphs := []Glyph{GlyphTerminal, GlyphEditor, GlyphFiles, GlyphHello, Glyph(99)}
	for _, g := range glyphs {
		buf, p := newPainter(tW, tH)
		for i := 0; i+3 < len(buf); i += 4 {
			buf[i], buf[i+1], buf[i+2], buf[i+3] = 0xC8, 0xC8, 0xC8, 0xFF
		}
		drawGlyph(p, g, toolkit.Rect{X: 10, Y: 10, W: IconGlyphPx, H: IconGlyphPx}, glyphInk)
		painted := 0
		for y := 10; y < 10+IconGlyphPx; y++ {
			for x := 10; x < 10+IconGlyphPx; x++ {
				off := (y*tW + x) * 4
				if buf[off] < 0x40 {
					painted++
				}
			}
		}
		if painted == 0 {
			t.Fatalf("glyph %v left no ink pixels", g)
		}
	}
}

// drawGlyph with a wider-than-tall box still paints its iconoir mark (the icon
// renders into the centred min(W,H) square).
func TestDrawGlyphWideBox(t *testing.T) {
	buf, p := newPainter(tW, tH)
	drawGlyph(p, GlyphHello, toolkit.Rect{X: 0, Y: 0, W: 20, H: 8}, glyphInk)
	painted := 0
	for i := range buf {
		if buf[i] != 0 {
			painted++
		}
	}
	if painted == 0 {
		t.Fatalf("glyph in wide box painted nothing")
	}
}

// An unknown glyph (no iconoir stem) falls back to a solid filled square.
func TestDrawGlyphUnknownFallsBackToSquare(t *testing.T) {
	buf, p := newPainter(tW, tH)
	r := toolkit.Rect{X: 4, Y: 4, W: IconGlyphPx, H: IconGlyphPx}
	drawGlyph(p, Glyph(99), r, glyphInk)
	// Every pixel inside the rect must be the (opaque) ink — a solid fill.
	for y := r.Y; y < r.Y+r.H; y++ {
		for x := r.X; x < r.X+r.W; x++ {
			off := (y*tW + x) * 4
			if buf[off] != glyphInk.R || buf[off+1] != glyphInk.G || buf[off+2] != glyphInk.B {
				t.Fatalf("unknown glyph not solid-filled at (%d,%d): %v", x, y, buf[off:off+3])
			}
		}
	}
}

// drawGlyph with a non-positive size is a no-op.
func TestDrawGlyphDegenerate(t *testing.T) {
	buf, p := newPainter(40, BarHeight)
	drawGlyph(p, GlyphTerminal, toolkit.Rect{X: 0, Y: 0, W: 0, H: 10}, glyphInk)
	drawGlyph(p, GlyphTerminal, toolkit.Rect{X: 0, Y: 0, W: 10, H: 0}, glyphInk)
	for _, b := range buf {
		if b != 0 {
			t.Fatalf("degenerate drawGlyph painted something: %d", b)
		}
	}
}

// gradientDir maps every Openbox gradient axis onto the toolkit.Backdrop
// direction, with Flat / unknown collapsing to the vertical default.
func TestGradientDir(t *testing.T) {
	cases := []struct {
		in   theme.GradientType
		want toolkit.GradientDir
	}{
		{theme.GradientVertical, toolkit.GradientVertical},
		{theme.GradientHorizontal, toolkit.GradientHorizontal},
		{theme.GradientDiagonal, toolkit.GradientDiagonal},
		{theme.GradientCrossDiagonal, toolkit.GradientCrossDiagonal},
		{theme.GradientFlat, toolkit.GradientVertical},
		{theme.GradientRaisedBevel, toolkit.GradientVertical},
	}
	for _, c := range cases {
		if got := gradientDir(c.in); got != c.want {
			t.Fatalf("gradientDir(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

// A section composes a toolkit.Backdrop, so its face is NOT a flat fill: the
// raised bevel makes the top row read brighter than the bottom row and the
// vertical gradient makes the top differ from the bottom in the interior. This
// asserts the gradient + bevel actually painted (i.e. the Backdrop composed the
// chrome the old paintBg / drawBevel used to hand-draw).
func TestSectionDrawBevelAndGradient(t *testing.T) {
	const w, h = 100, BarHeight
	buf, p := newPainter(w, h)
	sec := newSection()
	sec.bg = theme.Bg{
		Gradient: theme.GradientVertical,
		Color:    theme.Color{0x30, 0x30, 0x30},
		ColorTo:  theme.Color{0xC0, 0xC0, 0xC0},
	}
	sec.text.Set("1 of 4")
	sec.ink = theme.Color{0, 0, 0}
	sec.SetBounds(toolkit.Rect{X: 0, Y: 0, W: w, H: h})
	sec.Draw(p, dockToolkitTheme)

	// Sample an interior column away from the centred text so the glyph ink
	// does not pollute the gradient reading.
	col := 5
	lum := func(x, y int) int {
		off := (y*w + x) * 4
		return int(buf[off]) + int(buf[off+1]) + int(buf[off+2])
	}
	// The top bevel highlight row must be brighter than the bottom shadow row.
	if lum(col, 0) <= lum(col, h-1) {
		t.Fatalf("raised bevel not painted: top-row lum %d <= bottom-row lum %d",
			lum(col, 0), lum(col, h-1))
	}
	// The interior gradient must vary top -> bottom (Color -> ColorTo), so a
	// row just under the top bevel must be darker than one just above the
	// bottom bevel.
	if lum(col, 2) >= lum(col, h-3) {
		t.Fatalf("vertical gradient did not vary: upper lum %d >= lower lum %d",
			lum(col, 2), lum(col, h-3))
	}
}

// ---- narrow-surface + overflow render paths -------------------------------

// The dock clips its items to the iconbar rect on a narrow surface. Exercised
// by rendering on a narrow surface (must not panic and must paint the top-left
// border).
func TestRenderNarrowIconbarClipsButtons(t *testing.T) {
	s := New(220, BarHeight)
	buf := newBuf(s)
	Render(s, buf) // must not panic
	if buf[0] == 0 && buf[3] == 0 {
		t.Fatalf("narrow render did not paint top-left")
	}
}

// The dock stops painting once a launcher's anchor falls past the iconbar's
// right edge. Reproduced by stuffing in extra apps on a narrow surface.
func TestRenderStopsExtraIconbarButtons(t *testing.T) {
	s := New(400, BarHeight)
	s.Apps = []App{
		{Id: "a", Glyph: GlyphTerminal, Label: "A"},
		{Id: "b", Glyph: GlyphEditor, Label: "B"},
		{Id: "c", Glyph: GlyphFiles, Label: "C"},
	}
	buf := newBuf(s)
	Render(s, buf) // must not panic
}

// When the iconbar shrinks to width 0 the dock must not paint any item.
func TestRenderZeroWidthIconbar(t *testing.T) {
	s := New(WorkspaceW+ClockW, BarHeight) // iconbar collapses to 0
	buf := newBuf(s)
	Render(s, buf) // must not panic
}

// SetWindows stores the snapshot verbatim so the next render picks it up.
func TestSetWindowsStores(t *testing.T) {
	s := New(tW, tH)
	if len(s.Windows) != 0 {
		t.Fatalf("fresh state should have 0 windows, got %d", len(s.Windows))
	}
	s.SetWindows([]Window{
		{Id: 7, Title: "xterm", Focused: true},
		{Id: 12, Title: "editor", Minimized: true},
	})
	if got, want := len(s.Windows), 2; got != want {
		t.Fatalf("SetWindows length = %d, want %d", got, want)
	}
	if s.Windows[0].Id != 7 || s.Windows[0].Title != "xterm" || !s.Windows[0].Focused {
		t.Fatalf("Windows[0] = %+v, want {7 xterm focused}", s.Windows[0])
	}
	if !s.Windows[1].Minimized {
		t.Fatalf("Windows[1].Minimized = false, want true")
	}
	s.SetWindows(nil)
	if len(s.Windows) != 0 {
		t.Fatalf("SetWindows(nil) should clear, got %d", len(s.Windows))
	}
}

// An open window whose title maps to a launcher lights that launcher's running
// indicator (drawn by the dock) — the grouped-window model: no per-window
// button, the window collapses onto its launcher. Rendering with a focused,
// badged running window must not panic and the launcher must stay hit-testable.
func TestRenderRunningLauncherIndicator(t *testing.T) {
	s := New(tW, tH)
	s.SetWindows([]Window{{Id: 7, Title: "Terminal", Focused: true}})
	s.SetBadge("terminal", 3)
	buf := newBuf(s)
	Render(s, buf) // covers running + active + badge on the launcher
	// The terminal launcher (index 0) is running + focused-active.
	if run := s.launcherRunning(); !run[0] {
		t.Fatalf("terminal launcher should be running")
	}
	if s.focusedLauncher() != 0 {
		t.Fatalf("focused launcher = %d, want 0", s.focusedLauncher())
	}
	r := s.LauncherRects()[0]
	if got := s.HitTest(r[0]+r[2]/2, r[1]+r[3]/2); got != 0 {
		t.Fatalf("HitTest of running launcher = %d, want 0", got)
	}
}

// ---- workspaces -----------------------------------------------------------

// New defaults: ActiveWorkspace=1, WorkspaceCount=4, label="1 of 4".
func TestNewWorkspaceDefaults(t *testing.T) {
	s := New(tW, tH)
	if s.ActiveWorkspace != 1 {
		t.Fatalf("default ActiveWorkspace = %d, want 1", s.ActiveWorkspace)
	}
	if s.WorkspaceCount != 4 {
		t.Fatalf("default WorkspaceCount = %d, want 4", s.WorkspaceCount)
	}
	if s.Workspace != "1 of 4" {
		t.Fatalf("default Workspace label = %q, want %q", s.Workspace, "1 of 4")
	}
}

// SetActiveWorkspace clamps below + above the legal range, refreshes label.
func TestSetActiveWorkspaceClampsAndUpdatesLabel(t *testing.T) {
	s := New(tW, tH)
	s.SetActiveWorkspace(3)
	if s.ActiveWorkspace != 3 || s.Workspace != "3 of 4" {
		t.Fatalf("SetActiveWorkspace(3) = (%d,%q), want (3,%q)", s.ActiveWorkspace, s.Workspace, "3 of 4")
	}
	s.SetActiveWorkspace(0) // below range -> clamp to 1
	if s.ActiveWorkspace != 1 {
		t.Fatalf("SetActiveWorkspace(0) ActiveWorkspace = %d, want 1", s.ActiveWorkspace)
	}
	s.SetActiveWorkspace(99) // above range -> clamp to WorkspaceCount
	if s.ActiveWorkspace != s.WorkspaceCount {
		t.Fatalf("SetActiveWorkspace(99) ActiveWorkspace = %d, want %d", s.ActiveWorkspace, s.WorkspaceCount)
	}
}

// SetWorkspaceCount keeps ActiveWorkspace coherent and re-renders the label.
func TestSetWorkspaceCountRecomputesLabel(t *testing.T) {
	s := New(tW, tH)
	s.SetActiveWorkspace(4)
	s.SetWorkspaceCount(2) // active was 4 -> clamp down to 2
	if s.ActiveWorkspace != 2 || s.Workspace != "2 of 2" {
		t.Fatalf("after SetWorkspaceCount(2): (%d,%q), want (2,%q)", s.ActiveWorkspace, s.Workspace, "2 of 2")
	}
	s.SetWorkspaceCount(0)
	if s.Workspace != "2" {
		t.Fatalf("WorkspaceCount=0 label = %q, want %q", s.Workspace, "2")
	}
	s.SetWorkspaceCount(-1)
	if s.WorkspaceCount != 0 {
		t.Fatalf("WorkspaceCount after -1 = %d, want 0", s.WorkspaceCount)
	}
	s2 := New(tW, tH)
	s2.ActiveWorkspace = 0
	s2.SetWorkspaceCount(4)
	if s2.ActiveWorkspace != 1 {
		t.Fatalf("SetWorkspaceCount bump: ActiveWorkspace = %d, want 1", s2.ActiveWorkspace)
	}
}

// NextWorkspace + PrevWorkspace wrap at the boundaries.
func TestCycleWorkspaceWraps(t *testing.T) {
	s := New(tW, tH)
	if got := s.NextWorkspace(); got != 2 {
		t.Fatalf("NextWorkspace from 1/4 = %d, want 2", got)
	}
	s.SetActiveWorkspace(4)
	if got := s.NextWorkspace(); got != 1 {
		t.Fatalf("NextWorkspace from 4/4 = %d, want 1 (wrap)", got)
	}
	s.SetActiveWorkspace(1)
	if got := s.PrevWorkspace(); got != 4 {
		t.Fatalf("PrevWorkspace from 1/4 = %d, want 4 (wrap)", got)
	}
	s.SetActiveWorkspace(3)
	if got := s.PrevWorkspace(); got != 2 {
		t.Fatalf("PrevWorkspace from 3/4 = %d, want 2", got)
	}
}

// Non-positive count makes Next/Prev no-op (returns the current active).
func TestCycleWorkspaceNoCountIsNoop(t *testing.T) {
	s := New(tW, tH)
	s.SetWorkspaceCount(0)
	s.ActiveWorkspace = 7 // direct set; SetActiveWorkspace would not clamp w/ count=0
	if got := s.NextWorkspace(); got != 7 {
		t.Fatalf("NextWorkspace with count=0 = %d, want 7", got)
	}
	if got := s.PrevWorkspace(); got != 7 {
		t.Fatalf("PrevWorkspace with count=0 = %d, want 7", got)
	}
}

// HitTestWorkspace identifies clicks on the left section.
func TestHitTestWorkspace(t *testing.T) {
	s := New(tW, tH)
	if !s.HitTestWorkspace(WorkspaceW/2, tH/2) {
		t.Fatalf("center of workspace section not detected")
	}
	if s.HitTestWorkspace(WorkspaceW+10, tH/2) {
		t.Fatalf("iconbar click reported as workspace hit")
	}
	if s.HitTestWorkspace(tW-1, tH/2) {
		t.Fatalf("clock click reported as workspace hit")
	}
	if s.HitTestWorkspace(-5, tH/2) {
		t.Fatalf("negative-x click reported as workspace hit")
	}
}

// Render must paint the workspace label distinctly when ActiveWorkspace
// changes — the rendered ink for "3 of 4" differs from "1 of 4".
func TestRenderWorkspaceLabelChanges(t *testing.T) {
	s := New(tW, tH)
	buf1 := newBuf(s)
	Render(s, buf1)
	s.SetActiveWorkspace(3)
	buf2 := newBuf(s)
	Render(s, buf2)
	if bytesEqual(buf1, buf2) {
		t.Fatalf("workspace label did not change between workspace 1 and 3")
	}
}

// itoa exercises the zero, negative and multi-digit branches that the other
// tests do not naturally hit.
func TestItoa(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{{0, "0"}, {1, "1"}, {9, "9"}, {10, "10"}, {1234, "1234"}, {-1, "-1"}, {-42, "-42"}}
	for _, c := range cases {
		if got := itoa(c.in); got != c.want {
			t.Fatalf("itoa(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Window.Workspace round-trips through SetWindows (the compositor sends it
// in the windows_changed payload; the dock keeps it in the model).
func TestWindowCarriesWorkspaceField(t *testing.T) {
	s := New(tW, tH)
	s.SetWindows([]Window{{Id: 1, Title: "x", Workspace: 2}})
	if got := s.Windows[0].Workspace; got != 2 {
		t.Fatalf("Window.Workspace = %d, want 2", got)
	}
}

// TestSetTheme: SetTheme swaps the active theme + the next Render call uses
// the new colours.
func TestSetTheme(t *testing.T) {
	s := New(tW, tH)
	orig := s.Theme.Border.Color
	custom := theme.Theme{}
	custom.Border.Color = theme.Color{0xAB, 0xCD, 0xEF}
	s.SetTheme(custom)
	if s.Theme.Border.Color == orig {
		t.Fatalf("SetTheme did not swap the theme: still %v", s.Theme.Border.Color)
	}
	if s.Theme.Border.Color != (theme.Color{0xAB, 0xCD, 0xEF}) {
		t.Fatalf("SetTheme stored = %v", s.Theme.Border.Color)
	}
}

// rgba maps a theme.Color to an opaque toolkit.RGBA.
func TestRGBA(t *testing.T) {
	got := rgba(theme.Color{0x12, 0x34, 0x56})
	if got.R != 0x12 || got.G != 0x34 || got.B != 0x56 || got.A != 0xFF {
		t.Fatalf("rgba = %+v, want {0x12,0x34,0x56,0xFF}", got)
	}
}

// bytesEqual is a tiny []byte compare so the workspace render-change test
// does not pull in reflect.DeepEqual.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
