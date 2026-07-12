package core

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Findings can be read in more than one on-wire shape. Native is revpost's own
// object/array format; the two rdjson shapes are reviewdog's, for cheap interop
// with that ecosystem (linters, formatters, and agents that already emit it).
const (
	FormatNative  = "native"  // {body?, findings:[…]} or a bare findings array
	FormatRDJSON  = "rdjson"  // one object: {source?, diagnostics:[Diagnostic…]}
	FormatRDJSONL = "rdjsonl" // one rdjson Diagnostic per line (newline-delimited)
)

// ValidateFormat normalizes and checks the --format value. "" defaults to native.
// An unknown value is a validation error so a typo can never silently pick the
// wrong parser.
func ValidateFormat(s string) (string, error) {
	switch f := strings.ToLower(strings.TrimSpace(s)); f {
	case "", FormatNative:
		return FormatNative, nil
	case FormatRDJSON:
		return FormatRDJSON, nil
	case FormatRDJSONL:
		return FormatRDJSONL, nil
	default:
		return "", Validationf("bad-format",
			"--format must be native, rdjson, or rdjsonl (got %q)", s)
	}
}

// ParseFindings decodes the findings payload in the given (already-validated)
// format. It is the single entry the CLI calls; the format selects the parser.
func ParseFindings(data []byte, format string) (*Input, error) {
	switch format {
	case FormatRDJSON:
		return ParseRDJSON(data)
	case FormatRDJSONL:
		return ParseRDJSONL(data)
	default:
		return ParseInput(data)
	}
}

// rdDiagnostic is the subset of reviewdog's rdjson Diagnostic revpost maps:
// location.path, location.range (start/end line), message, and suggestions.
// Everything else (severity, source, code) decodes away — carried metadata
// revpost does not need. See https://github.com/reviewdog/reviewdog (rdjson).
type rdDiagnostic struct {
	Message  string `json:"message"`
	Location struct {
		Path  string   `json:"path"`
		Range *rdRange `json:"range"`
	} `json:"location"`
	Suggestions []rdSuggestion `json:"suggestions"`
}

// rdRange is a reviewdog text range. Lines are 1-based; column is 1-based with
// 0 (or absent) meaning "no column" — a range without columns covers whole lines.
type rdRange struct {
	Start rdPosition `json:"start"`
	End   rdPosition `json:"end"`
}

type rdPosition struct {
	Line   int `json:"line"`
	Column int `json:"column"`
}

// span normalizes the range to its 1-based line span: an absent end (or one
// before the start) collapses to the start line.
func (r rdRange) span() (start, end int) {
	start, end = r.Start.Line, r.End.Line
	if end < start {
		end = start
	}
	return start, end
}

// wholeLines reports whether the range replaces whole lines only: no column on
// either end. rdformat is linewise only when the columns are omitted (0/absent)
// — an explicit column, even 1, is character-precise (reviewdog's own reporter
// treats any column > 0 as a partial-line edit) — and GitHub suggestion blocks
// can only replace whole lines, so a column-precise range disqualifies.
func (r rdRange) wholeLines() bool {
	return r.Start.Column == 0 && r.End.Column == 0
}

// rdSuggestion is a fix the diagnostic proposes: text that replaces range.
type rdSuggestion struct {
	Range *rdRange `json:"range"`
	Text  string   `json:"text"`
}

// toRaw maps a diagnostic onto a rawFinding so it reuses the native validation
// (path/line/body checks, range collapse). reviewdog diagnostics describe the new
// file, so the side is always RIGHT. A range whose end line is past its start
// becomes a multi-line finding (revpost anchors on the last line); a point
// diagnostic or an end on the same line stays single-line. Suggestions are
// appended to the body as fenced blocks (see appendSuggestions).
func (d rdDiagnostic) toRaw() rawFinding {
	rf := rawFinding{Path: d.Location.Path, Body: d.appendSuggestions(d.Message), Side: SideRight}
	if r := d.Location.Range; r != nil {
		rf.Line = r.Start.Line
		if r.End.Line > r.Start.Line {
			start := r.Start.Line
			rf.StartLine = &start
			rf.Line = r.End.Line
		}
	}
	return rf
}

// appendSuggestions renders the diagnostic's suggestions under the message.
// GitHub applies a suggestion block to exactly the comment's anchored lines, so
// every suggestion that lines up with the anchor (whole lines, same span)
// becomes a ```suggestion block — blocks in one comment share the anchor, so
// they render as one-click alternatives and applying one outdates the rest. A
// suggestion that cannot line up folds in as a plain fenced block: a proposed
// fix is never silently dropped, it just loses one-click apply. Empty text is
// meaningful only as an aligned deletion — an empty plain fence says nothing,
// so a degenerate suggestion renders nothing at all. A message that leaves a
// code fence or a line-initial <!-- comment open would swallow the blocks, so
// the open construct is closed before them (see danglingCloser).
func (d rdDiagnostic) appendSuggestions(body string) string {
	blocks := make([]string, 0, len(d.Suggestions))
	for _, s := range d.Suggestions {
		switch {
		case d.aligned(s):
			blocks = append(blocks, fencedBlock("suggestion", s.Text))
		case s.Text != "":
			blocks = append(blocks, fencedBlock("", s.Text))
		}
	}
	if len(blocks) == 0 {
		return body
	}
	if body != "" {
		if f := danglingCloser(body); f != "" {
			body += "\n" + f
		}
		blocks = append([]string{body}, blocks...)
	}
	return strings.Join(blocks, "\n\n")
}

// aligned reports whether a suggestion replaces exactly the whole lines the
// comment anchors, i.e. whether GitHub would apply it where the fix belongs. A
// backwards range (end line before start) never qualifies — normalizing it
// away could promote a multi-line replacement onto a single anchored line.
func (d rdDiagnostic) aligned(s rdSuggestion) bool {
	r := s.Range
	if r == nil || d.Location.Range == nil || !r.wholeLines() {
		return false
	}
	if r.End.Line != 0 && r.End.Line < r.Start.Line {
		return false
	}
	sStart, sEnd := r.span()
	lStart, lEnd := d.Location.Range.span()
	return sStart == lStart && sEnd == lEnd
}

// danglingCloser returns the line(s) that close whatever the text leaves open —
// "" when nothing is open. Two CommonMark constructs would run past the end of
// the message and swallow appended text, matched closely enough for
// linter/agent-authored messages: a code fence (a run of 3+ backticks or tildes
// indented at most 3 spaces; only a bare run of the same character at least as
// long closes it — an info string is allowed on the opening fence only) and a
// line-initial <!-- comment (an HTML block, type 2, that runs until a line
// containing -->). The two suppress each other — inside a fence <!-- is literal
// text, inside a comment a fence marker is comment prose. A comment dangles at
// two layers: while the block is open a bare --> line joins it and lands in the
// raw HTML stream, but a close-then-reopen line ("a --> <!--") ends the block
// with a raw <!-- still unterminated, where an appended --> would be a
// paragraph (escaped, unreachable) — only a fresh <!-- --> line carries a
// literal terminator back into the HTML stream, and since HTML comments do not
// nest, one terminator always suffices. Scope: flat messages only — a fence or
// comment opened inside a list, blockquote, or other container is not tracked
// (a column-0 closer could not close it anyway).
func danglingCloser(text string) string {
	var open byte
	openLen := 0
	comment := false  // a line-initial <!-- block is still open
	htmlOpen := false // the block closed, but its line reopened a raw <!--
	for _, line := range strings.Split(text, "\n") {
		if comment {
			if strings.Contains(line, "-->") {
				comment = false
				htmlOpen = strings.LastIndex(line, "<!--") > strings.LastIndex(line, "-->")
			}
			continue
		}
		rest := strings.TrimLeft(line, " ")
		if len(line)-len(rest) > 3 || len(rest) < 3 {
			continue
		}
		if openLen == 0 && strings.HasPrefix(rest, "<!--") {
			if strings.Contains(rest, "-->") {
				htmlOpen = strings.LastIndex(rest, "<!--") > strings.LastIndex(rest, "-->")
			} else {
				comment = true
			}
			continue
		}
		if rest[0] != '`' && rest[0] != '~' {
			continue
		}
		c := rest[0]
		run := len(rest) - len(strings.TrimLeft(rest, string(c)))
		if run < 3 {
			continue
		}
		switch {
		case openLen == 0:
			open, openLen = c, run
		case c == open && run >= openLen && strings.TrimSpace(rest[run:]) == "":
			openLen = 0
		}
	}
	if comment {
		return "-->"
	}
	var closers []string
	if openLen > 0 {
		closers = append(closers, strings.Repeat(string(open), openLen))
	}
	if htmlOpen {
		closers = append(closers, "<!-- -->")
	}
	return strings.Join(closers, "\n")
}

// fencedBlock renders text as a fenced code block with the given info string
// ("suggestion" or none). The fence grows past the longest backtick run in the
// text so an embedded ``` cannot terminate the block early; empty text renders
// as an empty block (for a suggestion, that is a deletion), and a trailing
// newline is not doubled.
func fencedBlock(info, text string) string {
	size, run := 3, 0
	for _, r := range text {
		if r != '`' {
			run = 0
			continue
		}
		if run++; run >= size {
			size = run + 1
		}
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	fence := strings.Repeat("`", size)
	return fence + info + "\n" + text + fence
}

// ParseRDJSONL decodes newline-delimited rdjson diagnostics (one per line),
// mapping each to a validated Finding. Blank lines are skipped; a malformed line
// names its 1-based position, and any invalid mapped finding rejects the whole
// batch — revpost never posts a partial review from a broken input.
func ParseRDJSONL(data []byte) (*Input, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, Validationf("no-input",
			"no rdjsonl on stdin (pipe one rdjson diagnostic per line)")
	}
	in := &Input{Findings: []Finding{}}
	for lineNo, raw := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		var d rdDiagnostic
		if err := json.Unmarshal(raw, &d); err != nil {
			return nil, Validationf("bad-json", "rdjsonl line %d is malformed: %v", lineNo+1, err)
		}
		f, err := d.toRaw().validate(len(in.Findings))
		if err != nil {
			return nil, err
		}
		in.Findings = append(in.Findings, f)
	}
	return in, nil
}

// ParseRDJSON decodes the single-object rdjson form ({source?, diagnostics:[…]})
// and maps each diagnostic to a validated Finding. A well-formed object with no
// diagnostics is a valid empty batch (like an empty native array).
func ParseRDJSON(data []byte) (*Input, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, Validationf("no-input", "no rdjson on stdin (pipe a {\"diagnostics\":[…]} object)")
	}
	var doc struct {
		Diagnostics []rdDiagnostic `json:"diagnostics"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, Validationf("bad-json", "rdjson is malformed: %v", err)
	}
	in := &Input{Findings: make([]Finding, 0, len(doc.Diagnostics))}
	for i, d := range doc.Diagnostics {
		f, err := d.toRaw().validate(i)
		if err != nil {
			return nil, err
		}
		in.Findings = append(in.Findings, f)
	}
	return in, nil
}
