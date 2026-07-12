package core

import (
	"fmt"
	"strconv"
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

// A diagnostic suggestion whose range replaces exactly the anchored whole lines
// becomes a GitHub ```suggestion block appended to the message, so the fix is
// one-click-appliable.
func TestRDJSONSuggestionBecomesSuggestionBlock(t *testing.T) {
	src := `{"diagnostics":[{"message":"use fixed","location":{"path":"a.go","range":{"start":{"line":5}}},"suggestions":[{"range":{"start":{"line":5}},"text":"fixed"}]}]}`
	in, err := ParseRDJSON([]byte(src))
	if err != nil {
		t.Fatalf("ParseRDJSON: %v", err)
	}
	f := in.Findings[0]
	want := "use fixed\n\n```suggestion\nfixed\n```"
	if f.Body != want {
		t.Errorf("body =\n%q\nwant\n%q", f.Body, want)
	}
	if f.Line != 5 || f.StartLine != 0 {
		t.Errorf("anchor = %+v, want single-line 5", f)
	}
}

// A multi-line suggestion whose span equals the diagnostic's range keeps the
// multi-line anchor, so GitHub applies the block to all the anchored lines.
func TestRDJSONSuggestionOnMultiLineRange(t *testing.T) {
	src := `{"diagnostics":[{"message":"m","location":{"path":"a.go","range":{"start":{"line":5},"end":{"line":8}}},"suggestions":[{"range":{"start":{"line":5},"end":{"line":8}},"text":"x\ny"}]}]}`
	in, err := ParseRDJSON([]byte(src))
	if err != nil {
		t.Fatalf("ParseRDJSON: %v", err)
	}
	f := in.Findings[0]
	if f.StartLine != 5 || f.Line != 8 {
		t.Errorf("anchor = %+v, want range 5..8", f)
	}
	if want := "m\n\n```suggestion\nx\ny\n```"; f.Body != want {
		t.Errorf("body =\n%q\nwant\n%q", f.Body, want)
	}
}

// A suggestion that cannot be applied as a GitHub block — column-precise
// (rdformat is linewise only when columns are omitted, and an explicit column,
// even 1, is character-precise; GitHub suggestions replace whole lines only), a
// span that differs from the anchor, a malformed backwards range, or no range at
// all — is folded into the body as a plain fenced block instead, so the
// proposed fix is never silently dropped.
func TestRDJSONUnalignedSuggestionFoldsAsPlainFence(t *testing.T) {
	cases := map[string]string{
		"column-precise start":    `{"range":{"start":{"line":5,"column":3},"end":{"line":5}},"text":"fixed"}`,
		"explicit start column 1": `{"range":{"start":{"line":5,"column":1}},"text":"fixed"}`,
		"column-precise end":      `{"range":{"start":{"line":5},"end":{"line":5,"column":9}},"text":"fixed"}`,
		"span mismatch":           `{"range":{"start":{"line":5},"end":{"line":6}},"text":"fixed"}`,
		"reversed range":          `{"range":{"start":{"line":5},"end":{"line":3}},"text":"fixed"}`,
		"no range":                `{"text":"fixed"}`,
	}
	for name, sugg := range cases {
		t.Run(name, func(t *testing.T) {
			src := `{"diagnostics":[{"message":"m","location":{"path":"a.go","range":{"start":{"line":5}}},"suggestions":[` + sugg + `]}]}`
			in, err := ParseRDJSON([]byte(src))
			if err != nil {
				t.Fatalf("ParseRDJSON: %v", err)
			}
			f := in.Findings[0]
			if want := "m\n\n```\nfixed\n```"; f.Body != want {
				t.Errorf("body =\n%q\nwant\n%q", f.Body, want)
			}
		})
	}
}

// Every aligned suggestion becomes its own ```suggestion block — blocks in one
// comment share the anchor, so GitHub offers them as one-click alternatives
// (applying one outdates the rest). Unaligned suggestions fold as plain fences,
// in order.
func TestRDJSONEveryAlignedSuggestionBecomesBlock(t *testing.T) {
	aligned := `{"range":{"start":{"line":5}},"text":"%s"}`
	colPrecise := `{"range":{"start":{"line":5,"column":3},"end":{"line":5,"column":7}},"text":"%s"}`

	t.Run("two aligned: two blocks", func(t *testing.T) {
		src := `{"diagnostics":[{"message":"m","location":{"path":"a.go","range":{"start":{"line":5}}},"suggestions":[` +
			fmt.Sprintf(aligned, "one") + `,` + fmt.Sprintf(aligned, "two") + `]}]}`
		in, err := ParseRDJSON([]byte(src))
		if err != nil {
			t.Fatalf("ParseRDJSON: %v", err)
		}
		want := "m\n\n```suggestion\none\n```\n\n```suggestion\ntwo\n```"
		if f := in.Findings[0]; f.Body != want {
			t.Errorf("body =\n%q\nwant\n%q", f.Body, want)
		}
	})
	t.Run("first unaligned: folds, second still a block", func(t *testing.T) {
		src := `{"diagnostics":[{"message":"m","location":{"path":"a.go","range":{"start":{"line":5}}},"suggestions":[` +
			fmt.Sprintf(colPrecise, "one") + `,` + fmt.Sprintf(aligned, "two") + `]}]}`
		in, err := ParseRDJSON([]byte(src))
		if err != nil {
			t.Fatalf("ParseRDJSON: %v", err)
		}
		want := "m\n\n```\none\n```\n\n```suggestion\ntwo\n```"
		if f := in.Findings[0]; f.Body != want {
			t.Errorf("body =\n%q\nwant\n%q", f.Body, want)
		}
	})
}

// A degenerate suggestion — empty text without alignment (including a null
// array element, which decodes to the zero suggestion) — renders nothing:
// empty text is meaningful only as an aligned deletion, and an empty plain
// fence is junk. With an empty message too, nothing remains and the diagnostic
// is rejected exactly like a message-less diagnostic without suggestions.
func TestRDJSONDegenerateSuggestionsAreSkipped(t *testing.T) {
	for name, sugg := range map[string]string{
		"null suggestion":      `null`,
		"empty unaligned text": `{"text":""}`,
	} {
		t.Run(name, func(t *testing.T) {
			src := `{"diagnostics":[{"message":"m","location":{"path":"a.go","range":{"start":{"line":5}}},"suggestions":[` + sugg + `]}]}`
			in, err := ParseRDJSON([]byte(src))
			if err != nil {
				t.Fatalf("ParseRDJSON: %v", err)
			}
			if f := in.Findings[0]; f.Body != "m" {
				t.Errorf("body = %q, want %q (degenerate suggestion must render nothing)", f.Body, "m")
			}
		})
	}

	t.Run("empty message + degenerate suggestion is rejected", func(t *testing.T) {
		src := `{"diagnostics":[{"location":{"path":"a.go","range":{"start":{"line":5}}},"suggestions":[{"text":""}]}]}`
		_, err := ParseRDJSON([]byte(src))
		if err == nil {
			t.Fatal("want a validation error for a contentless diagnostic")
		}
		if ExitCode(err) != int(CodeValidation) {
			t.Errorf("exit = %d, want validation", ExitCode(err))
		}
	})
	t.Run("empty message + folded suggestion is still valid", func(t *testing.T) {
		src := `{"diagnostics":[{"location":{"path":"a.go","range":{"start":{"line":5}}},"suggestions":[{"text":"fixed"}]}]}`
		in, err := ParseRDJSON([]byte(src))
		if err != nil {
			t.Fatalf("ParseRDJSON: %v", err)
		}
		if want := "```\nfixed\n```"; in.Findings[0].Body != want {
			t.Errorf("body =\n%q\nwant\n%q", in.Findings[0].Body, want)
		}
	})
}

// A message that leaves a code fence open would swallow the appended blocks (a
// CommonMark closing fence may not carry an info string, so "```suggestion"
// cannot close it) — the dangling fence is closed before the first block. A
// balanced message is left alone, and so is a dangling one with no blocks to
// protect.
func TestRDJSONDanglingMessageFenceIsClosedBeforeBlocks(t *testing.T) {
	sugg := `,"suggestions":[{"range":{"start":{"line":5}},"text":"fixed"}]`
	block := "```suggestion\nfixed\n```"
	cases := []struct {
		name, message, suggJSON, want string
	}{
		{"dangling backtick fence", "bad:\n```", sugg, "bad:\n```\n```\n\n" + block},
		{"dangling tilde fence", "bad:\n~~~", sugg, "bad:\n~~~\n~~~\n\n" + block},
		{"longer dangling fence", "bad:\n`````", sugg, "bad:\n`````\n`````\n\n" + block},
		{"info-string line cannot close", "bad:\n```go\nx\n```go", sugg, "bad:\n```go\nx\n```go\n```\n\n" + block},
		{"balanced fence left alone", "ok:\n```go\nx\n```", sugg, "ok:\n```go\nx\n```\n\n" + block},
		{"no blocks: dangling left alone", "bad:\n```", "", "bad:\n```"},
		// A backtick fence's info string may not contain backticks, so a run
		// with more backticks later on the line is a paragraph with inline
		// code — a spurious closer here would itself open a real fence and
		// swallow the blocks. Tilde fence info strings may carry backticks.
		{"backtick run with a later backtick is inline code", "```foo``` bar", sugg, "```foo``` bar\n\n" + block},
		{"tilde fence info string may carry backticks", "~~~go`x", sugg, "~~~go`x\n~~~\n\n" + block},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := `{"diagnostics":[{"message":` + strconv.Quote(c.message) +
				`,"location":{"path":"a.go","range":{"start":{"line":5}}}` + c.suggJSON + `}]}`
			in, err := ParseRDJSON([]byte(src))
			if err != nil {
				t.Fatalf("ParseRDJSON: %v", err)
			}
			if f := in.Findings[0]; f.Body != c.want {
				t.Errorf("body =\n%q\nwant\n%q", f.Body, c.want)
			}
		})
	}
}

// A message whose line starts an unterminated <!-- comment swallows appended
// blocks just like an open fence: a line-initial <!-- opens a CommonMark HTML
// block (type 2) that runs until a line containing -->, or to the end of the
// document — and GitHub strips comment content, so the blocks vanish with no
// Apply button. The comment is closed before the blocks. Only a line-initial
// <!-- opens the block (a mid-line one stays inline and is harmless), and the
// two states suppress each other: a fence marker inside a comment is comment
// prose, a <!-- inside a fence is literal text.
func TestRDJSONDanglingMessageCommentIsClosedBeforeBlocks(t *testing.T) {
	sugg := `,"suggestions":[{"range":{"start":{"line":5}},"text":"fixed"}]`
	block := "```suggestion\nfixed\n```"
	cases := []struct {
		name, message, suggJSON, want string
	}{
		{"dangling comment", "bad:\n<!-- note", sugg, "bad:\n<!-- note\n-->\n\n" + block},
		{"comment closed on same line", "<!-- ok -->\nbad:", sugg, "<!-- ok -->\nbad:\n\n" + block},
		{"comment closed on later line", "<!--\nnote\n-->", sugg, "<!--\nnote\n-->\n\n" + block},
		{"empty comment closes itself", "<!-->\nbad:", sugg, "<!-->\nbad:\n\n" + block},
		{"mid-line comment stays inline", "bad: <!-- note", sugg, "bad: <!-- note\n\n" + block},
		{"four-space indent is not a comment", "bad:\n    <!--", sugg, "bad:\n    <!--\n\n" + block},
		{"fence inside comment is comment prose", "<!--\n```go\n-->", sugg, "<!--\n```go\n-->\n\n" + block},
		{"comment inside fence is literal text", "```\n<!--\n```", sugg, "```\n<!--\n```\n\n" + block},
		{"comment then dangling fence", "<!--\n-->\n```", sugg, "<!--\n-->\n```\n```\n\n" + block},
		{"no blocks: dangling comment left alone", "bad:\n<!-- note", "", "bad:\n<!-- note"},
		// A close-then-reopen on one line ends the markdown block but leaves a
		// raw <!-- open at the HTML layer, where a bare --> line can no longer
		// reach it (a paragraph escapes it) — only a fresh raw <!-- --> line
		// carries a literal terminator into the HTML stream.
		{"reopened after close on one line", "<!-- a --> <!--", sugg, "<!-- a --> <!--\n<!-- -->\n\n" + block},
		{"reopened on the block's closing line", "<!--\nb --> <!--", sugg, "<!--\nb --> <!--\n<!-- -->\n\n" + block},
		{"reopen terminated by a later comment line", "<!-- a --> <!--\n<!-- x -->", sugg, "<!-- a --> <!--\n<!-- x -->\n\n" + block},
		{"reopen then dangling fence needs both closers", "<!-- a --> <!--\n```go", sugg, "<!-- a --> <!--\n```go\n```\n<!-- -->\n\n" + block},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := `{"diagnostics":[{"message":` + strconv.Quote(c.message) +
				`,"location":{"path":"a.go","range":{"start":{"line":5}}}` + c.suggJSON + `}]}`
			in, err := ParseRDJSON([]byte(src))
			if err != nil {
				t.Fatalf("ParseRDJSON: %v", err)
			}
			if f := in.Findings[0]; f.Body != c.want {
				t.Errorf("body =\n%q\nwant\n%q", f.Body, c.want)
			}
		})
	}
}

// Suggestion text edge cases: a text carrying backtick fences needs a longer
// outer fence to survive rendering; a trailing newline is not doubled; empty
// text is a valid deletion suggestion (GitHub deletes the anchored lines).
func TestRDJSONSuggestionTextRendering(t *testing.T) {
	cases := []struct {
		name, text, want string
	}{
		{"backticks in text", "```suggestion\nnested\n```", "m\n\n````suggestion\n```suggestion\nnested\n```\n````"},
		{"four-run cascades to five", "````", "m\n\n`````suggestion\n````\n`````"},
		{"trailing newline not doubled", "fixed\n", "m\n\n```suggestion\nfixed\n```"},
		{"empty text is a deletion", "", "m\n\n```suggestion\n```"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := `{"diagnostics":[{"message":"m","location":{"path":"a.go","range":{"start":{"line":5}}},"suggestions":[{"range":{"start":{"line":5}},"text":` + strconv.Quote(c.text) + `}]}]}`
			in, err := ParseRDJSON([]byte(src))
			if err != nil {
				t.Fatalf("ParseRDJSON: %v", err)
			}
			if f := in.Findings[0]; f.Body != c.want {
				t.Errorf("body =\n%q\nwant\n%q", f.Body, c.want)
			}
		})
	}
}

// The rdjsonl path shares the same mapping, so a diagnostic line with an aligned
// suggestion translates identically.
func TestParseRDJSONLTranslatesSuggestion(t *testing.T) {
	line := `{"message":"m","location":{"path":"a.go","range":{"start":{"line":5}}},"suggestions":[{"range":{"start":{"line":5}},"text":"fixed"}]}`
	in, err := ParseRDJSONL([]byte(line))
	if err != nil {
		t.Fatalf("ParseRDJSONL: %v", err)
	}
	if want := "m\n\n```suggestion\nfixed\n```"; in.Findings[0].Body != want {
		t.Errorf("body =\n%q\nwant\n%q", in.Findings[0].Body, want)
	}
}

// A diagnostic with an empty message but an aligned suggestion is still a valid
// finding — the block alone is the body (a suggestion-only review comment).
func TestRDJSONSuggestionOnlyBodyIsValid(t *testing.T) {
	src := `{"diagnostics":[{"location":{"path":"a.go","range":{"start":{"line":5}}},"suggestions":[{"range":{"start":{"line":5}},"text":"fixed"}]}]}`
	in, err := ParseRDJSON([]byte(src))
	if err != nil {
		t.Fatalf("ParseRDJSON: %v", err)
	}
	if want := "```suggestion\nfixed\n```"; in.Findings[0].Body != want {
		t.Errorf("body =\n%q\nwant\n%q", in.Findings[0].Body, want)
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
