package core

import (
	"strings"
	"testing"
)

// The findings payload is accepted in two shapes: a top-level object
// {body?, findings:[…]} or a bare array of findings. Both normalize to the same
// Input, and an omitted side defaults to RIGHT.
func TestParseInputShapes(t *testing.T) {
	t.Run("object with summary body", func(t *testing.T) {
		in, err := ParseInput([]byte(`{"body":"summary","findings":[{"path":"a.go","line":5,"body":"msg"}]}`))
		if err != nil {
			t.Fatalf("ParseInput: %v", err)
		}
		if in.Body != "summary" {
			t.Errorf("Body = %q, want %q", in.Body, "summary")
		}
		if got := len(in.Findings); got != 1 {
			t.Fatalf("len(Findings) = %d, want 1", got)
		}
		f := in.Findings[0]
		if f.Path != "a.go" || f.Line != 5 || f.Body != "msg" {
			t.Errorf("finding = %+v, want {a.go 5 msg}", f)
		}
		if f.Side != SideRight {
			t.Errorf("Side = %q, want %q (default)", f.Side, SideRight)
		}
	})

	t.Run("bare array, side normalized to upper", func(t *testing.T) {
		in, err := ParseInput([]byte(`[{"path":"a.go","line":5,"body":"msg","side":"left"}]`))
		if err != nil {
			t.Fatalf("ParseInput: %v", err)
		}
		if in.Body != "" {
			t.Errorf("Body = %q, want empty for a bare array", in.Body)
		}
		if got := in.Findings[0].Side; got != SideLeft {
			t.Errorf("Side = %q, want %q", got, SideLeft)
		}
	})

	t.Run("unknown extra fields are ignored, not rejected", func(t *testing.T) {
		in, err := ParseInput([]byte(`[{"path":"a.go","line":5,"body":"msg","severity":"high","rule":"G101"}]`))
		if err != nil {
			t.Fatalf("ParseInput rejected unknown fields: %v", err)
		}
		if in.Findings[0].Path != "a.go" {
			t.Errorf("path lost: %+v", in.Findings[0])
		}
	})
}

// A well-formed-but-empty batch is valid input (the CLI turns it into an
// empty-result exit); only missing/malformed bytes are a usage error.
func TestParseInputEmptyIsValid(t *testing.T) {
	for _, src := range []string{`[]`, `{"findings":[]}`, `{}`} {
		in, err := ParseInput([]byte(src))
		if err != nil {
			t.Fatalf("ParseInput(%s): unexpected error %v", src, err)
		}
		if len(in.Findings) != 0 {
			t.Errorf("ParseInput(%s): want 0 findings, got %d", src, len(in.Findings))
		}
	}
}

// Missing or malformed bytes are a validation error (exit 2): fix the invocation,
// do not retry. This is distinct from a well-formed empty batch.
func TestParseInputMalformedIsValidationError(t *testing.T) {
	cases := map[string]string{
		"empty":           ``,
		"whitespace only": "  \n\t ",
		"not json":        `not json at all`,
		"truncated":       `{"findings":[`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseInput([]byte(src))
			if err == nil {
				t.Fatalf("ParseInput(%q) succeeded; want a validation error", src)
			}
			if got := ExitCode(err); got != int(CodeValidation) {
				t.Errorf("ExitCode = %d, want %d (validation)", got, int(CodeValidation))
			}
		})
	}
}

// Every finding is validated up front so a partial review is never posted from a
// malformed batch. Each bad-field case must name the offending finding index.
func TestParseInputFieldValidation(t *testing.T) {
	cases := map[string]string{
		"missing path":  `[{"line":5,"body":"msg"}]`,
		"blank path":    `[{"path":"  ","line":5,"body":"msg"}]`,
		"line below 1":  `[{"path":"a.go","line":0,"body":"msg"}]`,
		"missing body":  `[{"path":"a.go","line":5}]`,
		"blank body":    `[{"path":"a.go","line":5,"body":"   "}]`,
		"bad side":      `[{"path":"a.go","line":5,"body":"msg","side":"TOP"}]`,
		"path w/ newln": "[{\"path\":\"a\\ngo\",\"line\":5,\"body\":\"msg\"}]",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseInput([]byte(src))
			if err == nil {
				t.Fatalf("ParseInput(%s) succeeded; want a validation error", src)
			}
			if got := ExitCode(err); got != int(CodeValidation) {
				t.Errorf("ExitCode = %d, want %d (validation)", got, int(CodeValidation))
			}
			if !strings.Contains(err.Error(), "finding[0]") {
				t.Errorf("error should name the offending index: %q", err.Error())
			}
		})
	}
}

// Multi-line ranges are out of scope for v1 and must be rejected loudly — never
// silently downgraded to a single-line comment (which would misplace the fix).
func TestParseInputRejectsRangesLoudly(t *testing.T) {
	for _, src := range []string{
		`[{"path":"a.go","line":9,"body":"msg","start_line":5}]`,
		`[{"path":"a.go","line":9,"body":"msg","start_side":"RIGHT"}]`,
	} {
		_, err := ParseInput([]byte(src))
		if err == nil {
			t.Fatalf("ParseInput(%s) accepted a range; want a validation error", src)
		}
		if got := ExitCode(err); got != int(CodeValidation) {
			t.Errorf("ExitCode = %d, want %d (validation)", got, int(CodeValidation))
		}
		if !strings.Contains(strings.ToLower(err.Error()), "range") {
			t.Errorf("error should mention ranges: %q", err.Error())
		}
	}
}
