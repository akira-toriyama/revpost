package core

import (
	"strings"
	"testing"
)

// A single rdjson diagnostic maps to a single-line Finding: location.path ->
// Path, range.start.line -> Line, message -> Body, and side is always RIGHT
// (reviewdog diagnostics describe the new file).
func TestParseRDJSONLSingleDiagnostic(t *testing.T) {
	line := `{"message":"needs a nil check","location":{"path":"a.go","range":{"start":{"line":5,"column":2}}},"severity":"WARNING","source":{"name":"golangci-lint"},"code":{"value":"nilness"}}`
	in, err := ParseRDJSONL([]byte(line))
	if err != nil {
		t.Fatalf("ParseRDJSONL: %v", err)
	}
	if len(in.Findings) != 1 {
		t.Fatalf("Findings = %d, want 1", len(in.Findings))
	}
	f := in.Findings[0]
	if f.Path != "a.go" || f.Line != 5 || f.Body != "needs a nil check" || f.Side != SideRight {
		t.Errorf("finding = %+v, want {a.go 5 'needs a nil check' RIGHT}", f)
	}
	if f.StartLine != 0 {
		t.Errorf("a point diagnostic must not be a range: StartLine = %d, want 0", f.StartLine)
	}
	// The unmapped rdjson fields (severity, source, code) are ignored, not an error.
}

// A diagnostic whose range spans lines becomes a multi-line Finding: start.line
// -> StartLine, end.line -> Line (revpost anchors a range on its last line).
func TestParseRDJSONLMapsRange(t *testing.T) {
	line := `{"message":"m","location":{"path":"a.go","range":{"start":{"line":5},"end":{"line":8}}}}`
	in, err := ParseRDJSONL([]byte(line))
	if err != nil {
		t.Fatalf("ParseRDJSONL: %v", err)
	}
	f := in.Findings[0]
	if f.StartLine != 5 || f.Line != 8 {
		t.Errorf("range finding = %+v, want start 5 / line 8", f)
	}
}

// end.line == start.line (a single-line range with only columns differing) is not
// a multi-line range — it stays a single-line comment.
func TestParseRDJSONLEndEqualsStartIsSingleLine(t *testing.T) {
	line := `{"message":"m","location":{"path":"a.go","range":{"start":{"line":5,"column":1},"end":{"line":5,"column":9}}}}`
	in, err := ParseRDJSONL([]byte(line))
	if err != nil {
		t.Fatalf("ParseRDJSONL: %v", err)
	}
	if f := in.Findings[0]; f.Line != 5 || f.StartLine != 0 {
		t.Errorf("finding = %+v, want line 5 / StartLine 0", f)
	}
}

// Multiple diagnostics, one per line, map in order; blank lines are skipped.
func TestParseRDJSONLMultipleLinesInOrder(t *testing.T) {
	src := strings.Join([]string{
		`{"message":"first","location":{"path":"a.go","range":{"start":{"line":1}}}}`,
		``,
		`{"message":"second","location":{"path":"b.go","range":{"start":{"line":2}}}}`,
		`   `,
		`{"message":"third","location":{"path":"c.go","range":{"start":{"line":3}}}}`,
	}, "\n")
	in, err := ParseRDJSONL([]byte(src))
	if err != nil {
		t.Fatalf("ParseRDJSONL: %v", err)
	}
	if len(in.Findings) != 3 {
		t.Fatalf("Findings = %d, want 3", len(in.Findings))
	}
	for i, want := range []struct {
		path string
		line int
	}{{"a.go", 1}, {"b.go", 2}, {"c.go", 3}} {
		if in.Findings[i].Path != want.path || in.Findings[i].Line != want.line {
			t.Errorf("finding[%d] = %+v, want %v", i, in.Findings[i], want)
		}
	}
}

// A blank/whitespace-only payload is a validation error (no input), mirroring the
// native parser — not a silently-empty batch.
func TestParseRDJSONLEmptyIsValidationError(t *testing.T) {
	for _, src := range []string{"", "   \n\t\n "} {
		_, err := ParseRDJSONL([]byte(src))
		if err == nil {
			t.Fatalf("ParseRDJSONL(%q) succeeded; want a validation error", src)
		}
		if ExitCode(err) != int(CodeValidation) {
			t.Errorf("ParseRDJSONL(%q) exit = %d, want validation", src, ExitCode(err))
		}
	}
}

// A malformed JSON line is rejected loudly, naming the 1-based line so the
// offending record can be found; the whole batch is rejected (never partial).
func TestParseRDJSONLMalformedLineNamesLine(t *testing.T) {
	src := `{"message":"ok","location":{"path":"a.go","range":{"start":{"line":1}}}}` + "\n" + `{not json`
	_, err := ParseRDJSONL([]byte(src))
	if err == nil {
		t.Fatal("want a validation error for a malformed line")
	}
	if ExitCode(err) != int(CodeValidation) {
		t.Errorf("exit = %d, want validation", ExitCode(err))
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Errorf("error should name the offending line: %q", err.Error())
	}
}

// A diagnostic missing its path/message/line is validated exactly like a native
// finding, with a per-record index in the message.
func TestParseRDJSONLValidatesMappedFinding(t *testing.T) {
	cases := map[string]string{
		"missing path":    `{"message":"m","location":{"range":{"start":{"line":1}}}}`,
		"missing message": `{"location":{"path":"a.go","range":{"start":{"line":1}}}}`,
		"no range/line":   `{"message":"m","location":{"path":"a.go"}}`,
		"line below 1":    `{"message":"m","location":{"path":"a.go","range":{"start":{"line":0}}}}`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseRDJSONL([]byte(src))
			if err == nil {
				t.Fatalf("ParseRDJSONL(%s) succeeded; want a validation error", src)
			}
			if ExitCode(err) != int(CodeValidation) {
				t.Errorf("exit = %d, want validation", ExitCode(err))
			}
		})
	}
}

// The rdjson (non-l) form is a single object with a diagnostics array; it maps
// the same way, ignoring the top-level source.
func TestParseRDJSONObject(t *testing.T) {
	src := `{"source":{"name":"golangci-lint"},"diagnostics":[
		{"message":"one","location":{"path":"a.go","range":{"start":{"line":1}}}},
		{"message":"two","location":{"path":"b.go","range":{"start":{"line":2},"end":{"line":4}}}}
	]}`
	in, err := ParseRDJSON([]byte(src))
	if err != nil {
		t.Fatalf("ParseRDJSON: %v", err)
	}
	if len(in.Findings) != 2 {
		t.Fatalf("Findings = %d, want 2", len(in.Findings))
	}
	if in.Findings[0].Line != 1 || in.Findings[1].StartLine != 2 || in.Findings[1].Line != 4 {
		t.Errorf("findings = %+v", in.Findings)
	}
}

func TestParseRDJSONMalformedIsValidationError(t *testing.T) {
	for _, src := range []string{"", "not json", `{"diagnostics":`} {
		_, err := ParseRDJSON([]byte(src))
		if err == nil {
			t.Fatalf("ParseRDJSON(%q) succeeded; want a validation error", src)
		}
		if ExitCode(err) != int(CodeValidation) {
			t.Errorf("ParseRDJSON(%q) exit = %d, want validation", src, ExitCode(err))
		}
	}
}

// A well-formed rdjson object with no diagnostics is a valid empty batch (like an
// empty native array) — the CLI turns it into an empty-result exit, not an error.
func TestParseRDJSONEmptyDiagnosticsIsValid(t *testing.T) {
	in, err := ParseRDJSON([]byte(`{"diagnostics":[]}`))
	if err != nil {
		t.Fatalf("ParseRDJSON: %v", err)
	}
	if len(in.Findings) != 0 {
		t.Errorf("Findings = %d, want 0", len(in.Findings))
	}
}

func TestValidateFormat(t *testing.T) {
	for in, want := range map[string]string{
		"":          FormatNative,
		"native":    FormatNative,
		"NATIVE":    FormatNative,
		"rdjson":    FormatRDJSON,
		"rdjsonl":   FormatRDJSONL,
		" rdjsonl ": FormatRDJSONL,
	} {
		if got, err := ValidateFormat(in); err != nil || got != want {
			t.Errorf("ValidateFormat(%q) = (%q, %v), want (%q, nil)", in, got, err, want)
		}
	}
	for _, s := range []string{"json", "rdformat", "sarif"} {
		if _, err := ValidateFormat(s); err == nil {
			t.Errorf("ValidateFormat(%q) succeeded; want a validation error", s)
		} else if ExitCode(err) != int(CodeValidation) {
			t.Errorf("ValidateFormat(%q) exit = %d, want validation", s, ExitCode(err))
		}
	}
}

// ParseFindings dispatches on the (already-validated) format.
func TestParseFindingsDispatches(t *testing.T) {
	native := `[{"path":"a.go","line":5,"body":"msg"}]`
	rdl := `{"message":"m","location":{"path":"a.go","range":{"start":{"line":5}}}}`

	if in, err := ParseFindings([]byte(native), FormatNative); err != nil || len(in.Findings) != 1 {
		t.Errorf("native dispatch: in=%v err=%v", in, err)
	}
	if in, err := ParseFindings([]byte(rdl), FormatRDJSONL); err != nil || in.Findings[0].Line != 5 {
		t.Errorf("rdjsonl dispatch: in=%v err=%v", in, err)
	}
	if in, err := ParseFindings([]byte(`{"diagnostics":[`+rdl+`]}`), FormatRDJSON); err != nil || len(in.Findings) != 1 {
		t.Errorf("rdjson dispatch: in=%v err=%v", in, err)
	}
}
