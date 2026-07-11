package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Side is the diff side a comment anchors to: RIGHT is the new file (additions
// and context), LEFT is the old file (deletions and context). GitHub defaults an
// omitted side to RIGHT, and so does revpost.
const (
	SideRight = "RIGHT"
	SideLeft  = "LEFT"
)

// Finding is one inbound review comment after parse+validation, before its
// anchor is checked against the diff. Side is always normalized to RIGHT or LEFT.
//
// StartLine is 0 for a single-line comment; when > 0 the finding is a multi-line
// range from StartLine to Line on Side (both endpoints share the side). A range
// always satisfies 1 <= StartLine < Line — a start_line equal to line collapses
// to a single-line comment at parse time.
type Finding struct {
	Path      string
	Line      int
	Body      string
	Side      string
	StartLine int
}

// Input is the findings payload read from stdin: an optional review summary Body
// plus the findings to anchor and post.
type Input struct {
	Body     string
	Findings []Finding
}

// rawFinding mirrors the on-wire finding. start_line/start_side are pointers so an
// absent range is distinguishable from a zero value; unknown fields decode away
// silently so callers may carry their own metadata (severity, rule, …).
type rawFinding struct {
	Path      string  `json:"path"`
	Line      int     `json:"line"`
	Body      string  `json:"body"`
	Side      string  `json:"side"`
	StartLine *int    `json:"start_line"`
	StartSide *string `json:"start_side"`
}

// ParseInput decodes the findings JSON — a top-level object {body?, findings:[…]}
// or a bare array of findings — and validates every finding up front. A
// well-formed but empty batch is valid (the CLI turns it into an empty result);
// missing/malformed bytes and any bad finding are validation errors, so revpost
// never posts a partial review from a broken batch.
func ParseInput(data []byte) (*Input, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, Validationf("no-input",
			"no findings JSON on stdin (pipe a findings array or {\"findings\":[…]})")
	}

	var raw struct {
		Body     string       `json:"body"`
		Findings []rawFinding `json:"findings"`
	}
	switch trimmed[0] {
	case '[':
		if err := json.Unmarshal(trimmed, &raw.Findings); err != nil {
			return nil, Validationf("bad-json", "findings JSON is malformed: %v", err)
		}
	case '{':
		if err := json.Unmarshal(trimmed, &raw); err != nil {
			return nil, Validationf("bad-json", "findings JSON is malformed: %v", err)
		}
	default:
		return nil, Validationf("bad-json",
			"findings JSON must be an array or an object, got %q", string(trimmed[0]))
	}

	in := &Input{Body: raw.Body, Findings: make([]Finding, 0, len(raw.Findings))}
	for i, rf := range raw.Findings {
		f, err := rf.validate(i)
		if err != nil {
			return nil, err
		}
		in.Findings = append(in.Findings, f)
	}
	return in, nil
}

// validate turns a rawFinding into a Finding or a CodeValidation error naming the
// offending index, so an agent can fix exactly the finding at fault.
func (rf rawFinding) validate(i int) (Finding, error) {
	if strings.TrimSpace(rf.Path) == "" {
		return Finding{}, Validationf("bad-finding", "finding[%d]: path is required", i)
	}
	if strings.ContainsAny(rf.Path, "\n\r") {
		return Finding{}, Validationf("bad-finding", "finding[%d]: path must not contain newlines", i)
	}
	if rf.Line < 1 {
		return Finding{}, Validationf("bad-finding", "finding[%d]: line must be >= 1, got %d", i, rf.Line)
	}
	if strings.TrimSpace(rf.Body) == "" {
		return Finding{}, Validationf("bad-finding", "finding[%d]: body is required", i)
	}
	side, err := normalizeSide(rf.Side)
	if err != nil {
		return Finding{}, Validationf("bad-finding", "finding[%d]: %v", i, err)
	}
	startLine, err := rf.validateRange(i, side)
	if err != nil {
		return Finding{}, err
	}
	return Finding{Path: rf.Path, Line: rf.Line, Body: rf.Body, Side: side, StartLine: startLine}, nil
}

// validateRange resolves the optional multi-line range (start_line/start_side)
// and returns its start line — 0 for a single-line finding. A range must sit on
// the same side as the anchor (start_side, if given, must match side) and start
// at or before the end line. A start_line equal to line is a zero-length range,
// which GitHub rejects, so it degenerates to a single-line comment (returns 0).
func (rf rawFinding) validateRange(i int, side string) (int, error) {
	if rf.StartLine == nil {
		if rf.StartSide != nil {
			return 0, Validationf("bad-range", "finding[%d]: start_side given without start_line", i)
		}
		return 0, nil
	}
	start := *rf.StartLine
	if start < 1 {
		return 0, Validationf("bad-range", "finding[%d]: start_line must be >= 1, got %d", i, start)
	}
	if start > rf.Line {
		return 0, Validationf("bad-range",
			"finding[%d]: start_line (%d) must be <= line (%d)", i, start, rf.Line)
	}
	if rf.StartSide != nil {
		startSide, err := normalizeSide(*rf.StartSide)
		if err != nil {
			return 0, Validationf("bad-range", "finding[%d]: start_%v", i, err)
		}
		// A zero-length range (start == line) collapses to a single-line comment
		// below, where start_side plays no role — so enforce the side match only for
		// a real (start < line) range. An invalid start_side value is still rejected
		// above, so a typo never slips through even on a collapse.
		if start < rf.Line && startSide != side {
			return 0, Validationf("bad-range",
				"finding[%d]: start_side (%s) must match side (%s)", i, startSide, side)
		}
	}
	if start == rf.Line {
		return 0, nil // zero-length range → single-line comment
	}
	return start, nil
}

// normalizeSide upper-cases and defaults the side; an unrecognized value is an
// error so a typo can never silently anchor to the wrong side of the diff.
func normalizeSide(s string) (string, error) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "", SideRight:
		return SideRight, nil
	case SideLeft:
		return SideLeft, nil
	default:
		return "", fmt.Errorf("side must be RIGHT or LEFT, got %q", s)
	}
}
