package core

import (
	"fmt"
	"strings"
)

// Options controls how a finding's anchor is handled when it does not fall on a
// commentable diff line.
type Options struct {
	// SnapWithin, when >= 1, snaps a stray anchor to the nearest commentable line
	// within this many lines; 0 disables snapping (the finding drops instead).
	SnapWithin int
	// FoldDropped folds a non-snappable finding into the review body instead of
	// dropping it, so a finding is never lost.
	FoldDropped bool
	// Event is the review event: COMMENT, REQUEST_CHANGES, or APPROVE.
	Event string
}

// Comment is one verified inline comment, ready to post. Its fields are the exact
// wire shape GitHub's review API expects.
type Comment struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Side string `json:"side"`
	Body string `json:"body"`
}

// Review is the batched review payload — one POST replaces the per-comment dance.
// CommitID pins the review to the head the diff was computed from, so a push that
// lands between fetching the diff and posting cannot turn a verified anchor into a
// 422 (GitHub then validates anchors against that commit, not the moved head).
type Review struct {
	Event    string    `json:"event"`
	Body     string    `json:"body,omitempty"`
	CommitID string    `json:"commit_id,omitempty"`
	Comments []Comment `json:"comments"`
}

// Snap records a finding whose anchor was moved to a nearby commentable line.
type Snap struct {
	Path string `json:"path"`
	From int    `json:"from"`
	To   int    `json:"to"`
}

// Drop records a finding that could not be anchored and was discarded.
type Drop struct {
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Reason string `json:"reason"`
}

// Fold records a finding that was folded into the review body instead of dropped.
type Fold struct {
	Path string `json:"path"`
	Line int    `json:"line"`
}

// Plan is the outcome of verifying every finding against the diff: the review to
// post plus the machine-readable account of what happened to each finding. The
// three report slices are always non-nil so the report renders as [] not null.
type Plan struct {
	Review  Review
	Snapped []Snap
	Dropped []Drop
	Folded  []Fold
}

// BuildPlan verifies each finding's anchor against the commentable set, in input
// order, and assembles the review to post. For a non-commentable anchor it snaps
// (if enabled and within range), else folds (if enabled), else drops — each
// choice recorded. The review body is the input body, with any folded findings
// appended so nothing is silently lost.
func BuildPlan(in *Input, cs *CommentSet, opts Options) *Plan {
	p := &Plan{
		Review:  Review{Event: opts.Event, Comments: []Comment{}},
		Snapped: []Snap{},
		Dropped: []Drop{},
		Folded:  []Fold{},
	}
	var folded []Finding

	for _, f := range in.Findings {
		switch {
		case cs.Commentable(f.Path, f.Line, f.Side):
			p.Review.Comments = append(p.Review.Comments, Comment{
				Path: f.Path, Line: f.Line, Side: f.Side, Body: f.Body,
			})
		case opts.SnapWithin >= 1 && snap(cs, f, opts.SnapWithin, p):
			// handled inside snap
		case opts.FoldDropped:
			p.Folded = append(p.Folded, Fold{Path: f.Path, Line: f.Line})
			folded = append(folded, f)
		default:
			p.Dropped = append(p.Dropped, Drop{
				Path: f.Path, Line: f.Line, Reason: dropReason(cs, f.Path, f.Side),
			})
		}
	}

	p.Review.Body = composeBody(in.Body, folded)
	return p
}

// Report is the machine-readable account printed to stdout: how many comments
// were posted, and what happened to every finding that was not posted as-is.
type Report struct {
	Posted    int     `json:"posted"`
	Snapped   []Snap  `json:"snapped"`
	Dropped   []Drop  `json:"dropped"`
	Folded    []Fold  `json:"folded"`
	ReviewURL *string `json:"review_url"`
}

// Report renders the plan for stdout. reviewURL is nil on a dry-run or when
// nothing was posted, and marshals to JSON null. Posted counts every inline
// comment, snapped ones included — they are re-anchored, not dropped.
func (p *Plan) Report(reviewURL *string) Report {
	return Report{
		Posted:    len(p.Review.Comments),
		Snapped:   p.Snapped,
		Dropped:   p.Dropped,
		Folded:    p.Folded,
		ReviewURL: reviewURL,
	}
}

// snap moves f to the nearest commentable line within window; on success it
// appends the (re-anchored) comment and a Snap record and returns true.
func snap(cs *CommentSet, f Finding, window int, p *Plan) bool {
	to, ok := cs.Nearest(f.Path, f.Line, f.Side, window)
	if !ok {
		return false
	}
	p.Review.Comments = append(p.Review.Comments, Comment{
		Path: f.Path,
		Line: to,
		Side: f.Side,
		Body: fmt.Sprintf("(re: line %d)\n\n%s", f.Line, f.Body),
	})
	p.Snapped = append(p.Snapped, Snap{Path: f.Path, From: f.Line, To: to})
	return true
}

// dropReason names why an anchor could not be placed, precisely enough that each
// answer implies a different fix: the file is not in the PR, the file has no
// commentable line on that side at all (patch-less binary/rename, or a RIGHT
// anchor on a pure-deletion file), or the line simply falls outside the hunks.
func dropReason(cs *CommentSet, path, side string) string {
	switch {
	case !cs.HasPath(path):
		return "file not in diff"
	case !cs.HasLines(path, side):
		return "file has no commentable lines on this side"
	default:
		return "line not in diff"
	}
}

// composeBody appends a "findings outside the diff" section to the review body
// for every folded finding, keeping the operator's summary (if any) on top.
func composeBody(base string, folded []Finding) string {
	if len(folded) == 0 {
		return base
	}
	var b strings.Builder
	if base != "" {
		b.WriteString(base)
		b.WriteString("\n\n---\n")
	}
	b.WriteString("### Findings outside the diff\n")
	for _, f := range folded {
		fmt.Fprintf(&b, "- `%s:%d` — %s\n", f.Path, f.Line, indentContinuation(f.Body))
	}
	return b.String()
}

// indentContinuation indents every line after the first by two spaces so a
// multi-line finding body stays inside its markdown bullet instead of breaking
// the list (or injecting a sibling section).
func indentContinuation(body string) string {
	return strings.ReplaceAll(body, "\n", "\n  ")
}
