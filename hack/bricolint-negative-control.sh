#!/usr/bin/env bash
# SPDX-License-Identifier: BSD-3-Clause
#
# Copyright (c) 2026, the wasmdock authors
#
# bricolint-negative-control.sh — prove the hand-drawn-UI guard actually BITES.
#
# A green bricolint run is only meaningful if the analyzer would flag a real
# regression. This script asserts exactly that, in three phases against a REAL
# painter-using site in this app (scene.Render, which holds a live
# *painter.PixelPainter named `p`):
#
#   1. clean tree                       -> guard exits 0
#   2. inject a raw `p.FillRect(...)`   -> guard exits NON-zero (it bit)
#      into scene.Render (no allow)
#   3. remove the injection             -> guard exits 0 again
#
# The edit is made in place and unconditionally restored by an EXIT trap, so an
# abort mid-run never leaves the injected line behind.
#
# Env:
#   BRICOLINT  path to the bricolint binary
#              (default: "$(go env GOPATH)/bin/bricolint")
set -euo pipefail

# Resolve repository root from this script's location (hack/ lives at the root),
# so the control runs the same from CI (working-directory: .) or by hand.
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/.." && pwd)"
cd "$root"

BRICOLINT="${BRICOLINT:-$(go env GOPATH)/bin/bricolint}"
if [ ! -x "$BRICOLINT" ]; then
  echo "negative-control: bricolint not found or not executable at: $BRICOLINT" >&2
  echo "  install it with: go install github.com/go-widgets/bricolint/cmd/bricolint@v0.1.0" >&2
  exit 3
fi
export GOWORK=off

# The anchor is a stable, unique line inside scene.Render where a painter named
# `p` is already in scope. We inject the raw primitive on the line AFTER it.
TARGET="internal/scene/scene.go"
ANCHOR='p := painter.NewPixelPainter(buf, s.W, s.H)'
INJECT=$'\tp.FillRect(toolkit.Rect{X: 0, Y: 0, W: 1, H: 1}, toolkit.RGBA{}) // bricolint-negative-control: raw hand-drawn primitive (must be flagged)'

if ! grep -qF "$ANCHOR" "$TARGET"; then
  echo "negative-control: anchor not found in $TARGET: $ANCHOR" >&2
  echo "  the injection site drifted; update ANCHOR to a real painter-using line." >&2
  exit 3
fi

# Restore the target ONLY once a real backup has been taken (RESTORE=1) and it
# is non-empty, so a failure before the cp below cannot make the trap copy an
# empty temp over $TARGET and wipe it.
BACKUP="$(mktemp)"
RESTORE=0
restore() { [ "$RESTORE" = 1 ] && [ -s "$BACKUP" ] && cp "$BACKUP" "$TARGET"; rm -f "$BACKUP"; return 0; }
trap restore EXIT
cp "$TARGET" "$BACKUP"; RESTORE=1

# guard runs the analyzer and returns its exit code (0 = clean, non-0 = flagged).
guard() {
  go vet -vettool="$BRICOLINT" ./... >/dev/null 2>&1
}

# --- Phase 1: clean tree must pass -----------------------------------------
if guard; then
  echo "negative-control [1/3] clean tree: guard exits 0 (as expected)"
else
  echo "negative-control [1/3] FAIL: guard flagged the CLEAN tree" >&2
  exit 1
fi

# --- Phase 2: inject a raw primitive; guard must bite ----------------------
# Insert INJECT immediately after the (unique) anchor line.
awk -v anchor="$ANCHOR" -v inject="$INJECT" '
  { print }
  index($0, anchor) { print inject }
' "$BACKUP" > "$TARGET"

if guard; then
  echo "negative-control [2/3] FAIL: guard did NOT flag an injected p.FillRect(...)" >&2
  echo "  the hand-drawn-UI guard is not biting; CI would give false confidence." >&2
  exit 2
else
  echo "negative-control [2/3] injected raw p.FillRect: guard exits non-zero (it bit)"
fi

# --- Phase 3: remove the injection; guard must pass again -------------------
restore
trap - EXIT
if guard; then
  echo "negative-control [3/3] restored tree: guard exits 0 (as expected)"
else
  echo "negative-control [3/3] FAIL: guard still flags after restore" >&2
  exit 1
fi

echo "negative-control: PASS — the bricolint guard bites on a real hand-drawn primitive."
