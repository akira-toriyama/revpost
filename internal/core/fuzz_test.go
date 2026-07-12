package core

import "testing"

// ParseInput must never panic on arbitrary bytes — it either returns a valid
// Input or a typed validation error. A crash here would be an agent-triggerable
// DoS on the stdin path.
func FuzzParseInput(f *testing.F) {
	f.Add(`{"findings":[{"path":"a.go","line":5,"body":"x"}]}`)
	f.Add(`[{"path":"a.go","line":5,"body":"x","side":"left"}]`)
	f.Add(`[{"path":"a.go","line":9,"body":"x","start_line":3}]`)
	f.Add(`[{"path":"a.go","line":9,"body":"x","start_line":9}]`)  // zero-length → collapses
	f.Add(`[{"path":"a.go","line":9,"body":"x","start_line":20}]`) // start > line → rejected
	f.Add(``)
	f.Add(`{`)
	f.Add(`[1,2,3]`)
	f.Add(`{"body":"","findings":null}`)
	f.Fuzz(func(t *testing.T, data string) {
		in, err := ParseInput([]byte(data))
		if err != nil {
			return
		}
		// On success every finding is fully normalized.
		for i, fnd := range in.Findings {
			if fnd.Path == "" || fnd.Line < 1 || fnd.Body == "" {
				t.Fatalf("finding[%d] passed validation but is not well-formed: %+v", i, fnd)
			}
			if fnd.Side != SideRight && fnd.Side != SideLeft {
				t.Fatalf("finding[%d] side not normalized: %q", i, fnd.Side)
			}
			// A range, when present, is always 1 <= start < line (equal collapses to 0).
			if fnd.StartLine != 0 && (fnd.StartLine < 1 || fnd.StartLine >= fnd.Line) {
				t.Fatalf("finding[%d] range not well-formed: start=%d line=%d", i, fnd.StartLine, fnd.Line)
			}
		}
	})
}

// ParseRDJSONL and ParseRDJSON must never panic on arbitrary bytes either — they
// are on the same agent-facing stdin path as ParseInput, and every finding they
// produce must be as fully normalized as a native one (side always RIGHT).
func FuzzParseRDJSON(f *testing.F) {
	f.Add(`{"message":"m","location":{"path":"a.go","range":{"start":{"line":5}}}}`)
	f.Add(`{"message":"m","location":{"path":"a.go","range":{"start":{"line":5},"end":{"line":8}}}}`)
	f.Add("l1\nl2\n\n")
	f.Add(`{"diagnostics":[{"message":"m","location":{"path":"a.go","range":{"start":{"line":1}}}}]}`)
	f.Add(`{"message":"m","location":{"path":"a.go","range":{"start":{"line":5}}},"suggestions":[{"range":{"start":{"line":5}},"text":"x` + "```" + `y"}]}`)
	f.Add(`{"diagnostics":[{"location":{"path":"a.go","range":{"start":{"line":2},"end":{"line":3}}},"suggestions":[{"range":{"start":{"line":2},"end":{"line":3,"column":4}},"text":""},{"text":"z"}]}]}`)
	f.Add(`{"message":"x\n` + "```" + `","location":{"path":"a.go","range":{"start":{"line":1}}},"suggestions":[{"range":{"start":{"line":1}},"text":"y"},{"range":{"start":{"line":1},"end":{"line":0}},"text":"y2"}]}`)
	f.Add(``)
	f.Add(`{`)
	f.Fuzz(func(t *testing.T, data string) {
		for _, parse := range []func([]byte) (*Input, error){ParseRDJSONL, ParseRDJSON} {
			in, err := parse([]byte(data))
			if err != nil {
				continue
			}
			for i, fnd := range in.Findings {
				if fnd.Path == "" || fnd.Line < 1 || fnd.Body == "" {
					t.Fatalf("finding[%d] passed validation but is not well-formed: %+v", i, fnd)
				}
				if fnd.Side != SideRight {
					t.Fatalf("finding[%d] rdjson side must be RIGHT: %q", i, fnd.Side)
				}
				if fnd.StartLine != 0 && (fnd.StartLine < 1 || fnd.StartLine >= fnd.Line) {
					t.Fatalf("finding[%d] range not well-formed: start=%d line=%d", i, fnd.StartLine, fnd.Line)
				}
			}
		}
	})
}

// BuildCommentSet + the query methods must never panic on a malformed patch, and
// a Nearest hit must itself be commentable (snapping never invents a bad anchor).
func FuzzBuildCommentSet(f *testing.F) {
	f.Add("@@ -5,3 +5,4 @@\n ctx\n-del\n+add\n+add2\n ctx2\n", 6)
	f.Add("@@ -1 +1 @@\n-x\n+y\n", 1)
	f.Add("not a patch at all", 3)
	f.Add("@@ garbage @@\n+line\n", 0)
	f.Add("", -4)
	f.Fuzz(func(t *testing.T, patch string, line int) {
		cs := BuildCommentSet([]File{{Path: "f", Patch: patch}})
		for _, side := range []string{SideRight, SideLeft, "bogus"} {
			cs.Commentable("f", line, side)
			if to, ok := cs.Nearest("f", line, side, 3); ok && !cs.Commentable("f", to, side) {
				t.Fatalf("Nearest returned %d on side %s but it is not commentable", to, side)
			}
		}
	})
}
