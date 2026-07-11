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
// location.path, location.range (start/end line), and message. Everything else
// (severity, source, code, suggestions) decodes away — carried metadata revpost
// does not need. See https://github.com/reviewdog/reviewdog (rdjson/rdjsonl).
type rdDiagnostic struct {
	Message  string `json:"message"`
	Location struct {
		Path  string `json:"path"`
		Range *struct {
			Start struct {
				Line int `json:"line"`
			} `json:"start"`
			End struct {
				Line int `json:"line"`
			} `json:"end"`
		} `json:"range"`
	} `json:"location"`
}

// toRaw maps a diagnostic onto a rawFinding so it reuses the native validation
// (path/line/body checks, range collapse). reviewdog diagnostics describe the new
// file, so the side is always RIGHT. A range whose end line is past its start
// becomes a multi-line finding (revpost anchors on the last line); a point
// diagnostic or an end on the same line stays single-line.
func (d rdDiagnostic) toRaw() rawFinding {
	rf := rawFinding{Path: d.Location.Path, Body: d.Message, Side: SideRight}
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
