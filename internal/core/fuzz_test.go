package core

import "testing"

// ParseInput must never panic on arbitrary bytes — it either returns a valid
// Input or a typed validation error. A crash here would be an agent-triggerable
// DoS on the stdin path.
func FuzzParseInput(f *testing.F) {
	f.Add(`{"findings":[{"path":"a.go","line":5,"body":"x"}]}`)
	f.Add(`[{"path":"a.go","line":5,"body":"x","side":"left"}]`)
	f.Add(`[{"path":"a.go","line":9,"body":"x","start_line":3}]`)
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
