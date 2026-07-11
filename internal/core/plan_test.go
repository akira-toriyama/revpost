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

// A file that is in the PR but has no commentable line on the requested side
// (patch-less binary/rename, or a RIGHT anchor on a pure-deletion file) gets a
// distinct reason from a stray line inside a real diff — the fixes differ.
func TestBuildPlanDropReasonForPatchlessFile(t *testing.T) {
	cs := BuildCommentSet([]File{
		{Path: "add.go", Patch: patchNetAdd},
		{Path: "bin.png", Patch: ""}, // in the PR, but no patch
	})
	in := input("",
		Finding{Path: "bin.png", Line: 1, Body: "x", Side: SideRight},  // no commentable lines
		Finding{Path: "add.go", Line: 999, Body: "y", Side: SideRight}, // line off the diff
	)
	p := BuildPlan(in, cs, Options{Event: "COMMENT"})

	byPath := map[string]Drop{}
	for _, d := range p.Dropped {
		byPath[d.Path] = d
	}
	if got := byPath["bin.png"].Reason; got == "line not in diff" || got == "file not in diff" {
		t.Errorf("patch-less file reason = %q, want a distinct 'no commentable lines' message", got)
	}
	if got := byPath["add.go"].Reason; got != "line not in diff" {
		t.Errorf("add.go reason = %q, want %q", got, "line not in diff")
	}
}

// A folded finding whose body spans multiple lines must stay inside its bullet:
// continuation lines are indented so the body can't break out of (or inject into)
// the "Findings outside the diff" list.
func TestBuildPlanFoldedMultilineBodyStaysInBullet(t *testing.T) {
	in := input("", Finding{Path: "add.go", Line: 999, Body: "first line\nHIJACK", Side: SideRight})
	p := BuildPlan(in, findingsCS(), Options{Event: "COMMENT", FoldDropped: true})
	if !strings.Contains(p.Review.Body, "first line\n  HIJACK") {
		t.Errorf("continuation line must be indented under the bullet; got:\n%s", p.Review.Body)
	}
	for _, ln := range strings.Split(p.Review.Body, "\n") {
		if ln == "HIJACK" {
			t.Errorf("a body line escaped to column 0 (list break / injection):\n%s", p.Review.Body)
		}
	}
}

// A range whose endpoints are both commentable and in the same hunk is posted
// with start_line/start_side alongside line/side.
func TestBuildPlanKeepsCommentableRange(t *testing.T) {
	in := input("", Finding{Path: "add.go", Line: 8, Body: "range bug", Side: SideRight, StartLine: 5})
	p := BuildPlan(in, findingsCS(), Options{Event: "COMMENT"})

	if len(p.Review.Comments) != 1 {
		t.Fatalf("Comments = %d, want 1", len(p.Review.Comments))
	}
	c := p.Review.Comments[0]
	if c.StartLine != 5 || c.Line != 8 || c.Side != SideRight || c.StartSide != SideRight {
		t.Errorf("range comment = %+v, want start 5 / line 8 / RIGHT / start_side RIGHT", c)
	}
	if len(p.Dropped) != 0 {
		t.Errorf("Dropped = %+v, want empty", p.Dropped)
	}
}

// A range with an off-diff endpoint drops as a whole span and is NEVER snapped —
// which endpoint should move is ambiguous — even with a wide snap window.
func TestBuildPlanRangeNeverSnapsAndDrops(t *testing.T) {
	in := input("", Finding{Path: "add.go", Line: 100, Body: "x", Side: SideRight, StartLine: 5})
	p := BuildPlan(in, findingsCS(), Options{Event: "COMMENT", SnapWithin: 50})

	if len(p.Review.Comments) != 0 {
		t.Fatalf("Comments = %d, want 0 (a range must not snap)", len(p.Review.Comments))
	}
	if len(p.Snapped) != 0 {
		t.Errorf("Snapped = %+v, want empty (ranges never snap)", p.Snapped)
	}
	if len(p.Dropped) != 1 || p.Dropped[0].StartLine != 5 || p.Dropped[0].Reason != "range end not in diff" {
		t.Errorf("Dropped = %+v, want one {start 5, reason 'range end not in diff'}", p.Dropped)
	}
}

// GitHub 422s a range whose endpoints straddle two hunks; revpost drops it with a
// reason distinct from an endpoint that is simply off the diff.
func TestBuildPlanRangeSpanningHunksDrops(t *testing.T) {
	cs := BuildCommentSet([]File{{Path: "g", Patch: patchTwoHunks}}) // RIGHT {1,2,3}|{9,10,11}
	in := input("", Finding{Path: "g", Line: 9, Body: "x", Side: SideRight, StartLine: 3})
	p := BuildPlan(in, cs, Options{Event: "COMMENT"})

	if len(p.Review.Comments) != 0 {
		t.Fatalf("Comments = %d, want 0 (spans two hunks)", len(p.Review.Comments))
	}
	if len(p.Dropped) != 1 || p.Dropped[0].Reason != "range spans multiple hunks" {
		t.Errorf("Dropped = %+v, want one reason 'range spans multiple hunks'", p.Dropped)
	}
}

// A folded range shows its full span (path:start-end) in the review body.
func TestBuildPlanRangeFoldedShowsSpan(t *testing.T) {
	in := input("", Finding{Path: "add.go", Line: 100, Body: "orphan range", Side: SideRight, StartLine: 90})
	p := BuildPlan(in, findingsCS(), Options{Event: "COMMENT", FoldDropped: true})

	if len(p.Folded) != 1 || p.Folded[0].StartLine != 90 {
		t.Errorf("Folded = %+v, want one with StartLine 90", p.Folded)
	}
	if !strings.Contains(p.Review.Body, "add.go:90-100") {
		t.Errorf("folded body must show the span add.go:90-100; got:\n%s", p.Review.Body)
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
	// Lock the section structure: operator summary on top, then the separator and
	// header, and the single-line location rendered as "path:line".
	if wantPrefix := "summary\n\n---\n### Findings outside the diff\n"; !strings.HasPrefix(p.Review.Body, wantPrefix) {
		t.Errorf("body must start with the summary then separator+header; got:\n%s", p.Review.Body)
	}
	if !strings.Contains(p.Review.Body, "add.go:100") {
		t.Errorf("single-line folded finding must render as add.go:100; got:\n%s", p.Review.Body)
	}
}

// A LEFT-side finding produces a LEFT comment, and a LEFT range carries
// start_side LEFT — the rest of the suite exercises only RIGHT, so a hardcoded
// Side:RIGHT in BuildPlan would otherwise slip through.
func TestBuildPlanLeftSide(t *testing.T) {
	cs := findingsCS() // add.go LEFT commentable {5,6,7}

	single := BuildPlan(input("", Finding{Path: "add.go", Line: 6, Body: "x", Side: SideLeft}), cs, Options{Event: "COMMENT"})
	if len(single.Review.Comments) != 1 || single.Review.Comments[0].Side != SideLeft {
		t.Fatalf("LEFT single = %+v, want one LEFT comment", single.Review.Comments)
	}

	rng := BuildPlan(input("", Finding{Path: "add.go", Line: 7, Body: "x", Side: SideLeft, StartLine: 5}), cs, Options{Event: "COMMENT"})
	if len(rng.Review.Comments) != 1 {
		t.Fatalf("LEFT range comments = %d, want 1", len(rng.Review.Comments))
	}
	if c := rng.Review.Comments[0]; c.Side != SideLeft || c.StartSide != SideLeft || c.StartLine != 5 {
		t.Errorf("LEFT range = %+v, want side/start_side LEFT, start 5", c)
	}
}

// A range with only its START off the diff, and a range with BOTH endpoints off,
// each drop with their own reason — mirrors of the off-diff-END case, so a
// start/end mix-up in rangeDropReason can't hide behind the one tested branch.
func TestBuildPlanRangeDropReasonsByEndpoint(t *testing.T) {
	cs := findingsCS() // add.go RIGHT {5,6,7,8}
	cases := []struct {
		name       string
		start, end int
		wantReason string
	}{
		{"start off, end on", 2, 6, "range start not in diff"},
		{"both endpoints off", 2, 3, "range endpoints not in diff"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := input("", Finding{Path: "add.go", Line: tc.end, Body: "x", Side: SideRight, StartLine: tc.start})
			p := BuildPlan(in, cs, Options{Event: "COMMENT"})
			if len(p.Dropped) != 1 || p.Dropped[0].Reason != tc.wantReason {
				t.Errorf("Dropped = %+v, want one reason %q", p.Dropped, tc.wantReason)
			}
		})
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
