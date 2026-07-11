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
type Finding struct {
	Path string
	Line int
	Body string
	Side string
}

// Input is the findings payload read from stdin: an optional review summary Body
// plus the findings to anchor and post.
type Input struct {
	Body     string
	Findings []Finding
}

// rawFinding mirrors the on-wire finding. start_line/start_side are captured only
// to reject multi-line ranges loudly (v1 is single-line); unknown fields decode
// away silently so callers may carry their own metadata (severity, rule, …).
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
	if rf.StartLine != nil || rf.StartSide != nil {
		return Finding{}, Validationf("range-unsupported",
			"finding[%d]: multi-line ranges are not supported yet (v1) — drop start_line/start_side", i)
	}
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
	return Finding{Path: rf.Path, Line: rf.Line, Body: rf.Body, Side: side}, nil
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
