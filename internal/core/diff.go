package core

import (
	"regexp"
	"strconv"
	"strings"
)

// File is one changed file in the PR diff: its path and unified-diff patch.
// Patch is "" for files GitHub returns without one (binary, too large, or a pure
// rename) — such a file is part of the PR but contributes no commentable lines.
type File struct {
	Path  string
	Patch string
}

// CommentSet is the set of (path, line, side) anchors GitHub will accept as part
// of the diff, built from the PR's file patches. RIGHT holds new-file line
// numbers (additions and context); LEFT holds old-file line numbers (deletions
// and context). An anchor outside this set is what triggers the API's 422.
//
// Each commentable line is stored with the 1-based index of the hunk it belongs
// to (per side, per path), so a multi-line range can require both endpoints to
// sit in the SAME hunk — GitHub 422s a range that straddles two hunks. Hunks are
// disjoint, ordered stretches of a file, so a given (side, line) maps to exactly
// one hunk id.
type CommentSet struct {
	right map[string]map[int]int
	left  map[string]map[int]int
	paths map[string]struct{}
}

// hunkHeader captures the old-file and new-file start lines from "@@ -a,b +c,d @@"
// (the ,count is omitted for one-line hunks, hence the optional groups).
var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// BuildCommentSet walks each file's patch and records every commentable line per
// side. Every file's path is recorded even when it has no patch, so callers can
// tell "file not in the PR" from "line not in the diff".
func BuildCommentSet(files []File) *CommentSet {
	cs := &CommentSet{
		right: map[string]map[int]int{},
		left:  map[string]map[int]int{},
		paths: map[string]struct{}{},
	}
	for _, f := range files {
		cs.paths[f.Path] = struct{}{}
		cs.parsePatch(f.Path, f.Patch)
	}
	return cs
}

func (c *CommentSet) parsePatch(path, patch string) {
	if patch == "" {
		return
	}
	var oldLine, newLine, hunkID int
	inHunk := false
	for _, line := range strings.Split(patch, "\n") {
		if m := hunkHeader.FindStringSubmatch(line); m != nil {
			oldLine, _ = strconv.Atoi(m[1])
			newLine, _ = strconv.Atoi(m[2])
			hunkID++ // 1-based; endpoints of a range must share this id
			inHunk = true
			continue
		}
		if !inHunk || line == "" {
			continue
		}
		switch line[0] {
		case '+': // addition — new file only
			addCommentable(c.right, path, newLine, hunkID)
			newLine++
		case '-': // deletion — old file only
			addCommentable(c.left, path, oldLine, hunkID)
			oldLine++
		case ' ': // context — present on both sides
			addCommentable(c.right, path, newLine, hunkID)
			addCommentable(c.left, path, oldLine, hunkID)
			newLine++
			oldLine++
		case '\\':
			// "\ No newline at end of file" is an annotation on the prior line;
			// it consumes no line number on either side.
		default:
			// Unknown prefix (a malformed patch): ignore defensively rather than
			// desync the line counters.
		}
	}
}

func addCommentable(side map[string]map[int]int, path string, line, hunkID int) {
	m := side[path]
	if m == nil {
		m = map[int]int{}
		side[path] = m
	}
	m[line] = hunkID
}

func (c *CommentSet) sideMap(side string) map[string]map[int]int {
	if side == SideLeft {
		return c.left
	}
	return c.right
}

// HasPath reports whether the path is part of the PR at all (with or without a
// patch).
func (c *CommentSet) HasPath(path string) bool {
	_, ok := c.paths[path]
	return ok
}

// HasLines reports whether the path has any commentable line on the given side.
// It is false for a patch-less file (binary/too-large/pure rename) and for a
// RIGHT anchor on a pure-deletion file — cases where no line could ever match.
func (c *CommentSet) HasLines(path, side string) bool {
	return len(c.sideMap(side)[path]) > 0
}

// Commentable reports whether (path, line, side) is an anchor GitHub will accept.
func (c *CommentSet) Commentable(path string, line int, side string) bool {
	_, ok := c.hunkID(path, line, side)
	return ok
}

// hunkID returns the 1-based hunk index the (path, line, side) anchor belongs to,
// and whether the anchor is commentable at all. A multi-line range is valid only
// when both endpoints return the same id (same side, same hunk).
func (c *CommentSet) hunkID(path string, line int, side string) (int, bool) {
	m := c.sideMap(side)[path]
	if m == nil {
		return 0, false
	}
	h, ok := m[line]
	return h, ok
}

// Nearest returns the closest commentable line to `line` on (path, side) within
// `within` lines, and whether one was found. Ties (equal distance above and
// below) resolve to the smaller line so the result is deterministic. within < 1
// disables the search. The best-so-far is tracked with a `found` flag rather than
// a within-derived sentinel, so an arbitrarily large window (up to math.MaxInt,
// which ParseSnapWithin accepts) can't overflow into a bogus line-0 anchor.
func (c *CommentSet) Nearest(path string, line int, side string, within int) (int, bool) {
	m := c.sideMap(side)[path]
	if m == nil || within < 1 {
		return 0, false
	}
	best, bestDist, found := 0, 0, false
	for cand := range m {
		d := cand - line
		if d < 0 {
			d = -d
		}
		if d > within {
			continue
		}
		if !found || d < bestDist || (d == bestDist && cand < best) {
			best, bestDist, found = cand, d, true
		}
	}
	return best, found
}
