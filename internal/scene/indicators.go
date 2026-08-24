// SPDX-License-Identifier: BSD-3-Clause

package scene

import (
	"strings"
)

// Running / active indicators, attention badges, and workspace occupancy.
//
// A launcher whose app has at least one open window carries a small "running"
// dot centred under its glyph (the macOS/Unity dock convention); the launcher
// of the currently-focused window additionally gets a brighter active fill. An
// app can also request an attention badge — a count drawn in the launcher's
// top-right corner — through SetBadge. All three are drawn by the composed
// toolkit.AppDock from its AppDockItem flags; this file only computes the
// per-launcher model that syncView folds onto those items, plus the pager's
// per-workspace occupancy.

// appIndexForWindow maps an open window to the launcher index it belongs to, or
// -1 if none matches. The match is by the window's explicit App id when the
// compositor supplies one (Window.App, forward-compatible — the field rides the
// windows_changed payload the moment the compositor starts sending it), else by
// a case-insensitive match of the window Title against a launcher's Label or Id
// (the dom-app launch descriptors title their windows after the launcher, e.g.
// "VS Code", "Terminal", so the fallback lights the right dot today).
func (s *State) appIndexForWindow(w Window) int {
	if app := strings.TrimSpace(w.App); app != "" {
		for i := range s.Apps {
			if strings.EqualFold(s.Apps[i].Id, app) {
				return i
			}
		}
		return -1
	}
	title := strings.TrimSpace(w.Title)
	if title == "" {
		return -1
	}
	for i := range s.Apps {
		if strings.EqualFold(s.Apps[i].Label, title) || strings.EqualFold(s.Apps[i].Id, title) {
			return i
		}
	}
	return -1
}

// launcherRunning reports, per launcher index, whether at least one open (or
// folded) window maps to it — i.e. whether it should carry a running dot.
func (s *State) launcherRunning() []bool {
	out := make([]bool, len(s.Apps))
	for _, w := range s.Windows {
		if i := s.appIndexForWindow(w); i >= 0 {
			out[i] = true
		}
	}
	return out
}

// focusedLauncher returns the launcher index of the currently-focused window,
// or -1 if no focused window maps to a launcher. Its launcher gets the brighter
// active fill on top of the running dot.
func (s *State) focusedLauncher() int {
	for _, w := range s.Windows {
		if w.Focused {
			return s.appIndexForWindow(w)
		}
	}
	return -1
}

// workspaceOccupancy reports, per workspace cell (0-based, length
// WorkspaceCount), whether any open window is assigned to that workspace — the
// pager draws an occupancy dot on the true cells. A window's Workspace is
// 1-based; out-of-range or zero values are ignored. Returns nil when the count
// is non-positive, which the pager reads as "no dots".
func (s *State) workspaceOccupancy() []bool {
	if s.WorkspaceCount <= 0 {
		return nil
	}
	out := make([]bool, s.WorkspaceCount)
	for _, w := range s.Windows {
		if w.Workspace >= 1 && w.Workspace <= s.WorkspaceCount {
			out[w.Workspace-1] = true
		}
	}
	return out
}

// SetBadge sets (count > 0) or clears (count <= 0) the attention badge on the
// launcher whose Id is app. Unknown app ids are ignored. Placeholder attention
// API: a client requests a badge through the dock, which calls this and
// repaints; the count rides onto the launcher's AppDockItem.Badge in syncView.
func (s *State) SetBadge(app string, count int) {
	if s.badges == nil {
		s.badges = map[string]int{}
	}
	if count <= 0 {
		delete(s.badges, app)
		return
	}
	s.badges[app] = count
}

// BadgeCount returns the attention-badge count for the launcher app id (0 when
// none). Exposed so the wasm shell + tests can read back what SetBadge stored,
// and read by syncView to drive the dock item's Badge.
func (s *State) BadgeCount(app string) int {
	if s.badges == nil {
		return 0
	}
	return s.badges[app]
}
