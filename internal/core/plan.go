package core

import (
	"fmt"
	"strconv"
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
// wire shape GitHub's review API expects. StartLine/StartSide are set only for a
// multi-line range (the comment spans StartLine..Line); they are omitted for a
// single-line comment so the payload keeps its v1 shape.
type Comment struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Side      string `json:"side"`
	StartLine int    `json:"start_line,omitempty"`
	StartSide string `json:"start_side,omitempty"`
	Body      string `json:"body"`
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

// Drop records a finding that could not be anchored and was discarded. StartLine
// is set (and reported) only for a multi-line range, so the row describes the
// whole span that failed to anchor.
type Drop struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	StartLine int    `json:"start_line,omitempty"`
	Reason    string `json:"reason"`
}

// Fold records a finding that was folded into the review body instead of dropped.
// StartLine is set only for a multi-line range.
type Fold struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	StartLine int    `json:"start_line,omitempty"`
}

// ExistingComment is one inline review comment already on the PR. The idempotency
// guard (SkipExisting) uses it to drop a comment that was already posted — agents
// retry after timeouts, which would otherwise double-post. Its fields mirror the
// subset of a built Comment that identifies it.
type ExistingComment struct {
	Path      string
	Side      string
	Line      int
	StartLine int
	Body      string
}

// Skip records a comment that was not posted because an identical one is already
// on the PR (the idempotency guard). StartLine is set only for a range.
type Skip struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	StartLine int    `json:"start_line,omitempty"`
}

// Plan is the outcome of verifying every finding against the diff: the review to
// post plus the machine-readable account of what happened to each finding. The
// report slices are always non-nil so the report renders as [] not null.
type Plan struct {
	Review  Review
	Snapped []Snap
	Dropped []Drop
	Folded  []Fold
	Skipped []Skip
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
		Skipped: []Skip{},
	}
	var folded []Finding

	for _, f := range in.Findings {
		switch {
		case f.StartLine > 0 && rangeCommentable(cs, f):
			p.Review.Comments = append(p.Review.Comments, Comment{
				Path: f.Path, Line: f.Line, Side: f.Side,
				StartLine: f.StartLine, StartSide: f.Side, Body: f.Body,
			})
		case f.StartLine > 0:
			// A range never snaps — which endpoint moves is ambiguous — so an
			// off-diff range folds (if enabled) or drops as a whole span.
			if opts.FoldDropped {
				p.Folded = append(p.Folded, Fold{Path: f.Path, Line: f.Line, StartLine: f.StartLine})
				folded = append(folded, f)
			} else {
				p.Dropped = append(p.Dropped, Drop{
					Path: f.Path, Line: f.Line, StartLine: f.StartLine, Reason: rangeDropReason(cs, f),
				})
			}
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

// SkipExisting drops every built comment that already exists on the PR — an exact
// match on anchor (path, side, line, start_line) and body — recording each under
// Skipped. It is the idempotency guard: a retried run posts only genuinely new
// comments instead of double-posting after a timeout. The body compared is the
// built comment's, so a snapped comment's "(re: line N)" prefix is part of its
// identity. Existing comments that match nothing are ignored (they may be other
// reviewers'); a nil/empty set is a no-op, so a first post is never touched.
func (p *Plan) SkipExisting(existing []ExistingComment) {
	if len(existing) == 0 || len(p.Review.Comments) == 0 {
		return
	}
	seen := make(map[string]struct{}, len(existing))
	for _, e := range existing {
		seen[commentKey(e.Path, e.Side, e.Line, e.StartLine, e.Body)] = struct{}{}
	}
	kept := make([]Comment, 0, len(p.Review.Comments))
	for _, c := range p.Review.Comments {
		if _, dup := seen[commentKey(c.Path, c.Side, c.Line, c.StartLine, c.Body)]; dup {
			p.Skipped = append(p.Skipped, Skip{Path: c.Path, Line: c.Line, StartLine: c.StartLine})
			continue
		}
		kept = append(kept, c)
	}
	p.Review.Comments = kept
}

// commentKey is the identity of a posted comment for the idempotency guard: its
// anchor and exact body, joined on a NUL that cannot appear in a path or a side.
func commentKey(path, side string, line, startLine int, body string) string {
	return strings.Join([]string{
		path, side, strconv.Itoa(line), strconv.Itoa(startLine), body,
	}, "\x00")
}

// Report is the machine-readable account printed to stdout: how many comments
// were posted, and what happened to every finding that was not posted as-is.
type Report struct {
	Posted    int     `json:"posted"`
	Snapped   []Snap  `json:"snapped"`
	Dropped   []Drop  `json:"dropped"`
	Folded    []Fold  `json:"folded"`
	Skipped   []Skip  `json:"skipped"`
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
		Skipped:   p.Skipped,
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

// pathSideMiss classifies the two whole-file reasons an anchor cannot be placed
// at all — the file is not in the PR, or it has no commentable line on that side
// (patch-less binary/rename, or a RIGHT anchor on a pure-deletion file). It
// returns "" when the path does have commentable lines on the side, meaning the
// specific line or range is what failed. Shared by dropReason and rangeDropReason
// so the two literal reasons live in one place.
func pathSideMiss(cs *CommentSet, path, side string) string {
	switch {
	case !cs.HasPath(path):
		return "file not in diff"
	case !cs.HasLines(path, side):
		return "file has no commentable lines on this side"
	default:
		return ""
	}
}

// dropReason names why a single-line anchor could not be placed, precisely enough
// that each answer implies a different fix: a whole-file miss (pathSideMiss), or
// the line simply falls outside the hunks.
func dropReason(cs *CommentSet, path, side string) string {
	if r := pathSideMiss(cs, path, side); r != "" {
		return r
	}
	return "line not in diff"
}

// rangeCommentable reports whether a multi-line finding can be posted: both
// endpoints must be commentable on the finding's side AND belong to the same hunk
// (GitHub 422s a range whose ends straddle two hunks).
func rangeCommentable(cs *CommentSet, f Finding) bool {
	startH, ok1 := cs.hunkID(f.Path, f.StartLine, f.Side)
	endH, ok2 := cs.hunkID(f.Path, f.Line, f.Side)
	return ok1 && ok2 && startH == endH
}

// rangeDropReason explains why a range could not be anchored, distinguishing an
// endpoint off the diff from a range that spans two hunks — the fixes differ.
func rangeDropReason(cs *CommentSet, f Finding) string {
	if r := pathSideMiss(cs, f.Path, f.Side); r != "" {
		return r
	}
	startH, startOK := cs.hunkID(f.Path, f.StartLine, f.Side)
	endH, endOK := cs.hunkID(f.Path, f.Line, f.Side)
	switch {
	case !startOK && !endOK:
		return "range endpoints not in diff"
	case !startOK:
		return "range start not in diff"
	case !endOK:
		return "range end not in diff"
	case startH != endH:
		return "range spans multiple hunks"
	default:
		return "line not in diff" // unreachable: rangeCommentable would have kept it
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
		fmt.Fprintf(&b, "- `%s` — %s\n", foldLoc(f), indentContinuation(f.Body))
	}
	return b.String()
}

// foldLoc renders a folded finding's location: "path:line" for a single line,
// "path:start-end" for a multi-line range.
func foldLoc(f Finding) string {
	if f.StartLine > 0 {
		return fmt.Sprintf("%s:%d-%d", f.Path, f.StartLine, f.Line)
	}
	return fmt.Sprintf("%s:%d", f.Path, f.Line)
}

// indentContinuation indents every line after the first by two spaces so a
// multi-line finding body stays inside its markdown bullet instead of breaking
// the list (or injecting a sibling section).
func indentContinuation(body string) string {
	return strings.ReplaceAll(body, "\n", "\n  ")
}
