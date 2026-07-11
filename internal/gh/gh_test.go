package gh

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/akira-toriyama/revpost/internal/core"
)

// capture records what the fake runner was invoked with, so a test can assert the
// exact gh argv and the stdin payload.
type capture struct {
	args  []string
	stdin []byte
}

func fake(cap *capture, out []byte, err error) runner {
	return func(_ context.Context, stdin []byte, args ...string) ([]byte, error) {
		cap.args = args
		cap.stdin = stdin
		return out, err
	}
}

func TestFilesRequestsFilesAndDecodes(t *testing.T) {
	var cap capture
	body := `[{"filename":"a.go","patch":"@@ -1 +1 @@\n+x"},{"filename":"bin.png"}]`
	c := &Client{run: fake(&cap, []byte(body), nil)}

	files, err := c.Files(context.Background(), "o", "r", 7)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}

	wantArgs := []string{"api", "--paginate", "repos/o/r/pulls/7/files?per_page=100"}
	if strings.Join(cap.args, " ") != strings.Join(wantArgs, " ") {
		t.Errorf("argv = %v, want %v", cap.args, wantArgs)
	}
	if cap.stdin != nil {
		t.Errorf("GET must send no stdin, got %q", cap.stdin)
	}
	if len(files) != 2 {
		t.Fatalf("files = %d, want 2", len(files))
	}
	if files[0].Path != "a.go" || !strings.HasPrefix(files[0].Patch, "@@") {
		t.Errorf("files[0] = %+v", files[0])
	}
	// A patch-less file decodes to an empty Patch, not an error.
	if files[1].Path != "bin.png" || files[1].Patch != "" {
		t.Errorf("files[1] = %+v, want {bin.png ''}", files[1])
	}
}

func TestPostReviewSendsPayloadAndReturnsURL(t *testing.T) {
	var cap capture
	resp := `{"html_url":"https://github.com/o/r/pull/7#pullrequestreview-42"}`
	c := &Client{run: fake(&cap, []byte(resp), nil)}

	review := core.Review{
		Event: "COMMENT",
		Body:  "summary <b>",
		Comments: []core.Comment{
			{Path: "a.go", Line: 3, Side: core.SideRight, Body: "needs <fix>"},
		},
	}
	url, err := c.PostReview(context.Background(), "o", "r", 7, review)
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	if url != "https://github.com/o/r/pull/7#pullrequestreview-42" {
		t.Errorf("url = %q", url)
	}

	wantArgs := []string{"api", "--method", "POST", "repos/o/r/pulls/7/reviews", "--input", "-"}
	if strings.Join(cap.args, " ") != strings.Join(wantArgs, " ") {
		t.Errorf("argv = %v, want %v", cap.args, wantArgs)
	}

	// The payload goes on stdin as raw JSON with HTML left unescaped, so code and
	// diff content survive verbatim.
	if !strings.Contains(string(cap.stdin), "<fix>") || !strings.Contains(string(cap.stdin), "<b>") {
		t.Errorf("payload must keep HTML verbatim (no \\u003c): %s", cap.stdin)
	}
	var payload struct {
		Event    string `json:"event"`
		Body     string `json:"body"`
		Comments []struct {
			Path, Side, Body string
			Line             int
		} `json:"comments"`
	}
	if err := json.Unmarshal(cap.stdin, &payload); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if payload.Event != "COMMENT" || payload.Body != "summary <b>" {
		t.Errorf("payload = %+v", payload)
	}
	if len(payload.Comments) != 1 || payload.Comments[0].Line != 3 {
		t.Errorf("payload comments = %+v", payload.Comments)
	}
}

// An empty body is omitted from the payload entirely (an inline-only review),
// rather than sent as "body":"".
func TestPostReviewOmitsEmptyBody(t *testing.T) {
	var cap capture
	c := &Client{run: fake(&cap, []byte(`{"html_url":"u"}`), nil)}
	_, err := c.PostReview(context.Background(), "o", "r", 1, core.Review{
		Event:    "COMMENT",
		Comments: []core.Comment{{Path: "a.go", Line: 1, Side: core.SideRight, Body: "x"}},
	})
	if err != nil {
		t.Fatalf("PostReview: %v", err)
	}
	// Check the top-level review body key specifically (comments carry their own
	// body, so a substring match would be a false positive).
	var top map[string]json.RawMessage
	if err := json.Unmarshal(cap.stdin, &top); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if _, ok := top["body"]; ok {
		t.Errorf("empty review body must be omitted, got: %s", cap.stdin)
	}
}

// gh's HTTP status is mapped to the exit-code contract so scripts branch on it:
// 404 -> not found (1), 422 -> validation (2), anything else -> internal (3).
func TestErrorClassification(t *testing.T) {
	cases := []struct {
		stderr string
		want   int
	}{
		{"gh: Not Found (HTTP 404)", int(core.CodeNotFound)},
		{"gh: Validation Failed (HTTP 422)", int(core.CodeValidation)},
		{"gh: Bad credentials (HTTP 401)", int(core.CodeInternal)},
		{"some non-http failure", int(core.CodeInternal)},
	}
	for _, tc := range cases {
		t.Run(tc.stderr, func(t *testing.T) {
			c := &Client{run: fake(new(capture), nil, &execError{err: errors.New("exit 1"), stderr: tc.stderr})}
			_, err := c.Files(context.Background(), "o", "r", 1)
			if err == nil {
				t.Fatal("want an error")
			}
			if got := core.ExitCode(err); got != tc.want {
				t.Errorf("ExitCode = %d, want %d", got, tc.want)
			}
		})
	}
}

// A missing gh binary is a clear internal error naming the dependency.
func TestMissingGHBinary(t *testing.T) {
	c := &Client{run: fake(new(capture), nil, &execError{err: exec.ErrNotFound, stderr: ""})}
	_, err := c.Files(context.Background(), "o", "r", 1)
	if err == nil {
		t.Fatal("want an error")
	}
	if got := core.ExitCode(err); got != int(core.CodeInternal) {
		t.Errorf("ExitCode = %d, want internal", got)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "gh") {
		t.Errorf("error should name gh: %q", err.Error())
	}
}
