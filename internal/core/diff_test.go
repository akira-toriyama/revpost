package core

import (
	"math"
	"testing"
)

// A net-add hunk: one deletion, two additions around context. Additions and
// context are commentable on RIGHT (new-file line numbers); deletions and
// context are commentable on LEFT (old-file line numbers).
//
//	@@ -5,3 +5,4 @@   old 5..7 -> new 5..8
//	 ctx5     old5 / new5   (both)
//	-del6     old6          (LEFT)
//	+add_a    new6          (RIGHT)
//	+add_b    new7          (RIGHT)
//	 ctx7     old7 / new8   (both)
const patchNetAdd = "@@ -5,3 +5,4 @@\n ctx5\n-del6\n+add_a\n+add_b\n ctx7\n"

// A net-delete hunk shifts new-file numbers below old-file numbers, giving a
// clean LEFT-only anchor (old line 8) that is not a RIGHT line.
//
//	@@ -5,4 +5,3 @@
//	 ctx5     old5 / new5
//	-del6     old6          (LEFT)
//	-del7     old7          (LEFT)
//	+add_a    new6          (RIGHT)
//	 ctx8     old8 / new7
const patchNetDel = "@@ -5,4 +5,3 @@\n ctx5\n-del6\n-del7\n+add_a\n ctx8\n"

// Two hunks in one file give a gap in the RIGHT set: {1,2,3} (hunk 1) and
// {9,10,11} (hunk 2). A range straddling the gap spans two hunks — invalid.
const patchTwoHunks = "@@ -1,3 +1,3 @@\n a\n-b\n+B\n c\n@@ -20,3 +9,3 @@\n x\n-y\n+Y\n z\n"

func TestBuildCommentSetSides(t *testing.T) {
	cs := BuildCommentSet([]File{{Path: "add.go", Patch: patchNetAdd}})

	rightTrue := []int{5, 6, 7, 8} // ctx5, add_a, add_b, ctx7
	for _, ln := range rightTrue {
		if !cs.Commentable("add.go", ln, SideRight) {
			t.Errorf("RIGHT line %d should be commentable", ln)
		}
	}
	if cs.Commentable("add.go", 9, SideRight) {
		t.Error("RIGHT line 9 is past the hunk; must not be commentable")
	}
	// Line 8 is a clean RIGHT-only anchor: it is a new-file line, not an old one.
	if cs.Commentable("add.go", 8, SideLeft) {
		t.Error("line 8 is RIGHT-only; must not be commentable on LEFT")
	}

	leftTrue := []int{5, 6, 7} // ctx5, del6, ctx7(old)
	for _, ln := range leftTrue {
		if !cs.Commentable("add.go", ln, SideLeft) {
			t.Errorf("LEFT line %d should be commentable", ln)
		}
	}
}

func TestBuildCommentSetLeftOnlyAnchor(t *testing.T) {
	cs := BuildCommentSet([]File{{Path: "del.go", Patch: patchNetDel}})
	// Old line 8 (ctx8) maps to new line 7 — a clean LEFT-only anchor.
	if !cs.Commentable("del.go", 8, SideLeft) {
		t.Error("LEFT line 8 should be commentable")
	}
	if cs.Commentable("del.go", 8, SideRight) {
		t.Error("line 8 is LEFT-only; must not be commentable on RIGHT")
	}
}

func TestBuildCommentSetHunkHeaderWithoutCounts(t *testing.T) {
	// A one-line hunk omits the ,count in the header: "@@ -1 +1 @@".
	cs := BuildCommentSet([]File{{Path: "f", Patch: "@@ -1 +1 @@\n-old\n+new\n"}})
	if !cs.Commentable("f", 1, SideRight) || !cs.Commentable("f", 1, SideLeft) {
		t.Error("count-omitted header should still yield line 1 on both sides")
	}
}

func TestBuildCommentSetSkipsNoNewlineMarker(t *testing.T) {
	patch := "@@ -1 +1 @@\n-old\n\\ No newline at end of file\n+new\n\\ No newline at end of file\n"
	cs := BuildCommentSet([]File{{Path: "f", Patch: patch}})
	if !cs.Commentable("f", 1, SideRight) {
		t.Error(`the "\ No newline" marker must not shift or consume a line`)
	}
	if cs.Commentable("f", 2, SideRight) {
		t.Error("no second line exists; the marker must not have advanced the counter")
	}
}

func TestBuildCommentSetPathPresence(t *testing.T) {
	cs := BuildCommentSet([]File{
		{Path: "add.go", Patch: patchNetAdd},
		{Path: "bin.png", Patch: ""}, // binary/too-large: in the PR, but no patch
	})
	if !cs.HasPath("add.go") {
		t.Error("add.go is in the diff")
	}
	if !cs.HasPath("bin.png") {
		t.Error("a patch-less file is still part of the PR (distinguishes 'no patch' from 'not in PR')")
	}
	if cs.HasPath("absent.go") {
		t.Error("absent.go is not in the PR")
	}
	if cs.Commentable("bin.png", 1, SideRight) {
		t.Error("a patch-less file has no commentable lines")
	}
}

func TestNearest(t *testing.T) {
	cs := BuildCommentSet([]File{{Path: "add.go", Patch: patchNetAdd}}) // RIGHT {5,6,7,8}

	if got, ok := cs.Nearest("add.go", 11, SideRight, 3); !ok || got != 8 {
		t.Errorf("Nearest(11, within 3) = (%d, %v), want (8, true)", got, ok)
	}
	if _, ok := cs.Nearest("add.go", 11, SideRight, 2); ok {
		t.Error("Nearest(11, within 2): line 8 is 3 away; want no match")
	}
	// A RIGHT miss must never snap to a LEFT line.
	if _, ok := cs.Nearest("absent.go", 5, SideRight, 5); ok {
		t.Error("Nearest on an absent path must not match")
	}
}

// A multi-line range is valid only when both endpoints share a hunk. hunkID maps
// each commentable line to its 1-based hunk index, so endpoints in the same hunk
// match and endpoints across the gap do not.
func TestHunkIDGroupsByHunk(t *testing.T) {
	cs := BuildCommentSet([]File{{Path: "g", Patch: patchTwoHunks}}) // RIGHT {1,2,3}|{9,10,11}

	h1, ok1 := cs.hunkID("g", 1, SideRight)
	h3, ok3 := cs.hunkID("g", 3, SideRight)
	h9, ok9 := cs.hunkID("g", 9, SideRight)
	if !ok1 || !ok3 || !ok9 {
		t.Fatalf("lines 1, 3, 9 should all be commentable: %v %v %v", ok1, ok3, ok9)
	}
	if h1 != h3 {
		t.Errorf("lines 1 and 3 share a hunk; got ids %d and %d", h1, h3)
	}
	if h3 == h9 {
		t.Errorf("lines 3 and 9 are in different hunks; both got id %d", h3)
	}
	if _, ok := cs.hunkID("g", 5, SideRight); ok {
		t.Error("line 5 falls in the gap between hunks; must not be commentable")
	}
}

// ParseSnapWithin accepts an arbitrarily large window (e.g. within:<MaxInt>), so
// Nearest must not derive its distance sentinel by adding to `within` — that would
// overflow at math.MaxInt and return a bogus line-0 anchor instead of the true
// nearest commentable line.
func TestNearestHandlesMaxIntWindow(t *testing.T) {
	cs := BuildCommentSet([]File{{Path: "add.go", Patch: patchNetAdd}}) // RIGHT {5,6,7,8}
	if got, ok := cs.Nearest("add.go", 10, SideRight, math.MaxInt); !ok || got != 8 {
		t.Errorf("Nearest(10, within MaxInt) = (%d, %v), want (8, true)", got, ok)
	}
}

func TestNearestTieResolvesToSmallerLine(t *testing.T) {
	// Two hunks give a gap in the RIGHT set: {1,2,3} and {9,10,11}. Target 6 is
	// 3 from line 3 and 3 from line 9 — the tie must resolve to the smaller line.
	patch := "@@ -1,3 +1,3 @@\n a\n-b\n+B\n c\n@@ -20,3 +9,3 @@\n x\n-y\n+Y\n z\n"
	cs := BuildCommentSet([]File{{Path: "g", Patch: patch}})
	if got, ok := cs.Nearest("g", 6, SideRight, 3); !ok || got != 3 {
		t.Errorf("Nearest(6, within 3) = (%d, %v), want (3, true) on a tie", got, ok)
	}
	if _, ok := cs.Nearest("g", 6, SideRight, 2); ok {
		t.Error("Nearest(6, within 2): nearest lines are 3 away; want no match")
	}
}
