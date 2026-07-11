package core

import (
	"strings"
	"testing"
)

// findingsCS is patchNetAdd on "add.go": RIGHT commentable lines are {5,6,7,8}.
func findingsCS() *CommentSet {
	return BuildCommentSet([]File{{Path: "add.go", Patch: patchNetAdd}})
}

func input(body string, fs ...Finding) *Input { return &Input{Body: body, Findings: fs} }

func TestBuildPlanKeepsCommentableFindings(t *testing.T) {
	in := input("", Finding{Path: "add.go", Line: 6, Body: "bug", Side: SideRight})
	p := BuildPlan(in, findingsCS(), Options{Event: "COMMENT"})

	if got := len(p.Review.Comments); got != 1 {
		t.Fatalf("Comments = %d, want 1", got)
	}
	c := p.Review.Comments[0]
	if c.Path != "add.go" || c.Line != 6 || c.Side != SideRight || c.Body != "bug" {
		t.Errorf("comment = %+v, want {add.go 6 RIGHT bug}", c)
	}
	if p.Review.Event != "COMMENT" {
		t.Errorf("Event = %q, want COMMENT", p.Review.Event)
	}
}

// Report slices must be non-nil so the CLI emits [] (never null) — agents index
// them unconditionally.
func TestBuildPlanEmptySlicesNormalized(t *testing.T) {
	in := input("", Finding{Path: "add.go", Line: 6, Body: "bug", Side: SideRight})
	p := BuildPlan(in, findingsCS(), Options{Event: "COMMENT"})
	if p.Snapped == nil || p.Dropped == nil || p.Folded == nil {
		t.Errorf("report slices must be non-nil: snapped=%v dropped=%v folded=%v",
			p.Snapped, p.Dropped, p.Folded)
	}
}

func TestBuildPlanDropsWhenNotCommentableAndNoSnap(t *testing.T) {
	in := input("",
		Finding{Path: "add.go", Line: 100, Body: "x", Side: SideRight}, // in PR, wrong line
		Finding{Path: "ghost.go", Line: 3, Body: "y", Side: SideRight}, // not in PR
	)
	p := BuildPlan(in, findingsCS(), Options{Event: "COMMENT"})

	if len(p.Review.Comments) != 0 {
		t.Fatalf("Comments = %d, want 0", len(p.Review.Comments))
	}
	if len(p.Dropped) != 2 {
		t.Fatalf("Dropped = %d, want 2: %+v", len(p.Dropped), p.Dropped)
	}
	byPath := map[string]Drop{}
	for _, d := range p.Dropped {
		byPath[d.Path] = d
	}
	if got := byPath["add.go"].Reason; got != "line not in diff" {
		t.Errorf("add.go drop reason = %q, want %q", got, "line not in diff")
	}
	if got := byPath["ghost.go"].Reason; got != "file not in diff" {
		t.Errorf("ghost.go drop reason = %q, want %q", got, "file not in diff")
	}
}

func TestBuildPlanSnapsWithinWindow(t *testing.T) {
	// Line 10 is 2 away from the nearest commentable RIGHT line (8).
	in := input("", Finding{Path: "add.go", Line: 10, Body: "x", Side: SideRight})
	p := BuildPlan(in, findingsCS(), Options{Event: "COMMENT", SnapWithin: 3})

	if len(p.Review.Comments) != 1 {
		t.Fatalf("Comments = %d, want 1", len(p.Review.Comments))
	}
	c := p.Review.Comments[0]
	if c.Line != 8 {
		t.Errorf("snapped comment line = %d, want 8", c.Line)
	}
	if !strings.Contains(c.Body, "(re: line 10)") || !strings.Contains(c.Body, "x") {
		t.Errorf("snapped body must note the original line and keep the text: %q", c.Body)
	}
	if len(p.Snapped) != 1 || p.Snapped[0] != (Snap{Path: "add.go", From: 10, To: 8}) {
		t.Errorf("Snapped = %+v, want one {add.go 10 8}", p.Snapped)
	}
	if len(p.Dropped) != 0 {
		t.Errorf("Dropped = %+v, want empty", p.Dropped)
	}
}

func TestBuildPlanSnapOutOfWindowDrops(t *testing.T) {
	in := input("", Finding{Path: "add.go", Line: 20, Body: "x", Side: SideRight})
	p := BuildPlan(in, findingsCS(), Options{Event: "COMMENT", SnapWithin: 3})
	if len(p.Review.Comments) != 0 {
		t.Fatalf("Comments = %d, want 0 (too far to snap)", len(p.Review.Comments))
	}
	if len(p.Dropped) != 1 {
		t.Fatalf("Dropped = %d, want 1", len(p.Dropped))
	}
}

func TestBuildPlanFoldDroppedPreservesFinding(t *testing.T) {
	in := input("summary", Finding{Path: "add.go", Line: 100, Body: "orphan", Side: SideRight})
	p := BuildPlan(in, findingsCS(), Options{Event: "COMMENT", FoldDropped: true})

	if len(p.Review.Comments) != 0 {
		t.Fatalf("Comments = %d, want 0", len(p.Review.Comments))
	}
	if len(p.Dropped) != 0 {
		t.Errorf("Dropped = %+v; a folded finding must not also be dropped", p.Dropped)
	}
	if len(p.Folded) != 1 || p.Folded[0] != (Fold{Path: "add.go", Line: 100}) {
		t.Errorf("Folded = %+v, want one {add.go 100}", p.Folded)
	}
	for _, want := range []string{"summary", "add.go", "100", "orphan"} {
		if !strings.Contains(p.Review.Body, want) {
			t.Errorf("review body must contain %q; got:\n%s", want, p.Review.Body)
		}
	}
}

func TestBuildPlanBodyIsInputBodyWhenNothingFolded(t *testing.T) {
	in := input("just a summary", Finding{Path: "add.go", Line: 6, Body: "bug", Side: SideRight})
	p := BuildPlan(in, findingsCS(), Options{Event: "COMMENT"})
	if p.Review.Body != "just a summary" {
		t.Errorf("Body = %q, want the input body verbatim", p.Review.Body)
	}
}

// Report's posted count includes snapped comments (they were re-anchored and
// will be posted); review_url passes through and is nil on a dry-run.
func TestPlanReport(t *testing.T) {
	in := input("",
		Finding{Path: "add.go", Line: 6, Body: "a", Side: SideRight},   // keep
		Finding{Path: "add.go", Line: 10, Body: "b", Side: SideRight},  // snap -> 8
		Finding{Path: "ghost.go", Line: 1, Body: "c", Side: SideRight}, // drop
	)
	p := BuildPlan(in, findingsCS(), Options{Event: "COMMENT", SnapWithin: 3})

	dry := p.Report(nil)
	if dry.Posted != 2 {
		t.Errorf("Posted = %d, want 2 (kept + snapped)", dry.Posted)
	}
	if dry.ReviewURL != nil {
		t.Errorf("dry-run ReviewURL = %v, want nil", *dry.ReviewURL)
	}
	if len(dry.Snapped) != 1 || len(dry.Dropped) != 1 {
		t.Errorf("Snapped=%d Dropped=%d, want 1 and 1", len(dry.Snapped), len(dry.Dropped))
	}

	url := "https://github.com/o/r/pull/1#pullrequestreview-9"
	if got := p.Report(&url).ReviewURL; got == nil || *got != url {
		t.Errorf("ReviewURL = %v, want %q", got, url)
	}
}

func TestBuildPlanPreservesFindingOrder(t *testing.T) {
	in := input("",
		Finding{Path: "add.go", Line: 6, Body: "a", Side: SideRight},  // keep
		Finding{Path: "add.go", Line: 10, Body: "b", Side: SideRight}, // snap -> 8
		Finding{Path: "add.go", Line: 5, Body: "c", Side: SideRight},  // keep
	)
	p := BuildPlan(in, findingsCS(), Options{Event: "COMMENT", SnapWithin: 3})
	var lines []int
	for _, c := range p.Review.Comments {
		lines = append(lines, c.Line)
	}
	want := []int{6, 8, 5}
	if len(lines) != len(want) {
		t.Fatalf("comment lines = %v, want %v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("comment order = %v, want %v", lines, want)
			break
		}
	}
}
