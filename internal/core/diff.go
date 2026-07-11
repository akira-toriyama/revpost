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
type CommentSet struct {
	right map[string]map[int]struct{}
	left  map[string]map[int]struct{}
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
		right: map[string]map[int]struct{}{},
		left:  map[string]map[int]struct{}{},
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
	var oldLine, newLine int
	inHunk := false
	for _, line := range strings.Split(patch, "\n") {
		if m := hunkHeader.FindStringSubmatch(line); m != nil {
			oldLine, _ = strconv.Atoi(m[1])
			newLine, _ = strconv.Atoi(m[2])
			inHunk = true
			continue
		}
		if !inHunk || line == "" {
			continue
		}
		switch line[0] {
		case '+': // addition — new file only
			addCommentable(c.right, path, newLine)
			newLine++
		case '-': // deletion — old file only
			addCommentable(c.left, path, oldLine)
			oldLine++
		case ' ': // context — present on both sides
			addCommentable(c.right, path, newLine)
			addCommentable(c.left, path, oldLine)
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

func addCommentable(side map[string]map[int]struct{}, path string, line int) {
	m := side[path]
	if m == nil {
		m = map[int]struct{}{}
		side[path] = m
	}
	m[line] = struct{}{}
}

func (c *CommentSet) sideMap(side string) map[string]map[int]struct{} {
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
	m := c.sideMap(side)[path]
	if m == nil {
		return false
	}
	_, ok := m[line]
	return ok
}

// Nearest returns the closest commentable line to `line` on (path, side) within
// `within` lines, and whether one was found. Ties (equal distance above and
// below) resolve to the smaller line so the result is deterministic. within < 1
// disables the search.
func (c *CommentSet) Nearest(path string, line int, side string, within int) (int, bool) {
	m := c.sideMap(side)[path]
	if m == nil || within < 1 {
		return 0, false
	}
	best, bestDist := 0, within+1
	for cand := range m {
		d := cand - line
		if d < 0 {
			d = -d
		}
		if d > within {
			continue
		}
		if d < bestDist || (d == bestDist && cand < best) {
			best, bestDist = cand, d
		}
	}
	if bestDist > within {
		return 0, false
	}
	return best, true
}
