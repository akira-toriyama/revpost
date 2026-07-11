package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/akira-toriyama/revpost/internal/core"
)

// patchAdd is a net-add hunk on "add.go": RIGHT commentable lines are {5,6,7,8}.
const patchAdd = "@@ -5,3 +5,4 @@\n ctx5\n-del6\n+add_a\n+add_b\n ctx7\n"

// fakeSvc is an in-memory PRService: no network, records what was posted.
type fakeSvc struct {
	files     []core.File
	filesErr  error
	headSHA   string
	headErr   error
	url       string
	postErr   error
	posted    *core.Review
	postCalls int
}

func (f *fakeSvc) Files(context.Context, string, string, int) ([]core.File, error) {
	return f.files, f.filesErr
}

func (f *fakeSvc) HeadSHA(context.Context, string, string, int) (string, error) {
	return f.headSHA, f.headErr
}

func (f *fakeSvc) PostReview(_ context.Context, _, _ string, _ int, r core.Review) (string, error) {
	f.postCalls++
	f.posted = &r
	return f.url, f.postErr
}

type reportJSON struct {
	Posted  int `json:"posted"`
	Snapped []struct {
		Path     string `json:"path"`
		From, To int
	} `json:"snapped"`
	Dropped []struct {
		Path, Reason string
		Line         int
	} `json:"dropped"`
	Folded []struct {
		Path string
		Line int
	} `json:"folded"`
	ReviewURL *string `json:"review_url"`
}

// run drives the CLI with injected stdin/stdout/stderr and a fake service,
// returning captured streams and the process exit code.
func run(t *testing.T, svc core.PRService, stdin string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var o, e bytes.Buffer
	oldIn, oldOut, oldErr := in, out, errOut
	in, out, errOut = strings.NewReader(stdin), &o, &e
	defer func() { in, out, errOut = oldIn, oldOut, oldErr }()
	code = runWith(context.Background(), svc, args)
	return o.String(), e.String(), code
}

func decodeReport(t *testing.T, s string) reportJSON {
	t.Helper()
	var r reportJSON
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		t.Fatalf("stdout is not a report JSON: %v\n%s", err, s)
	}
	return r
}

func addGoSvc() *fakeSvc {
	return &fakeSvc{files: []core.File{{Path: "add.go", Patch: patchAdd}}, headSHA: "headsha1"}
}

func TestHelpFlagRendersGrammar(t *testing.T) {
	out, _, code := run(t, addGoSvc(), "", "--help")
	if code != 0 {
		t.Fatalf("--help exit = %d, want 0", code)
	}
	for _, want := range []string{"revpost", "--dry-run", "--snap"} {
		if !strings.Contains(out, want) {
			t.Errorf("help missing %q:\n%s", want, out)
		}
	}
}

// A bare invocation (no target) is a usage error — guidance on stderr, exit 2,
// and stdout stays clean (it carries the report only).
func TestBareInvocationIsUsageError(t *testing.T) {
	out, errStr, code := run(t, addGoSvc(), "")
	if code != int(core.CodeValidation) {
		t.Fatalf("bare invocation exit = %d, want 2 (usage)", code)
	}
	if out != "" {
		t.Errorf("stdout must stay clean, got: %s", out)
	}
	if !strings.Contains(errStr, "owner/repo#N") {
		t.Errorf("stderr should guide toward the target grammar, got: %s", errStr)
	}
}

func TestDryRunReportsWithoutPosting(t *testing.T) {
	svc := addGoSvc()
	out, errStr, code := run(t, svc, `{"findings":[{"path":"add.go","line":6,"body":"bug"}]}`,
		"o/r#7", "--dry-run")
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, errStr)
	}
	if svc.postCalls != 0 {
		t.Errorf("dry-run must not post, got %d calls", svc.postCalls)
	}
	if errStr != "" {
		t.Errorf("stderr must be clean on success, got: %s", errStr)
	}
	r := decodeReport(t, out)
	if r.Posted != 1 {
		t.Errorf("posted = %d, want 1", r.Posted)
	}
	if r.ReviewURL != nil {
		t.Errorf("dry-run review_url = %v, want null", *r.ReviewURL)
	}
}

func TestPostsAndReportsURL(t *testing.T) {
	svc := addGoSvc()
	svc.url = "https://github.com/o/r/pull/7#pullrequestreview-1"
	out, _, code := run(t, svc, `{"findings":[{"path":"add.go","line":6,"body":"bug"}]}`, "o/r#7")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if svc.postCalls != 1 {
		t.Fatalf("post calls = %d, want 1", svc.postCalls)
	}
	if got := len(svc.posted.Comments); got != 1 || svc.posted.Comments[0].Line != 6 {
		t.Errorf("posted comments = %+v", svc.posted.Comments)
	}
	if svc.posted.CommitID != "headsha1" {
		t.Errorf("review must be pinned to the head SHA, got CommitID = %q", svc.posted.CommitID)
	}
	r := decodeReport(t, out)
	if r.ReviewURL == nil || *r.ReviewURL != svc.url {
		t.Errorf("review_url = %v, want %q", r.ReviewURL, svc.url)
	}
}

func TestEmptyResultExitsOneWithoutPosting(t *testing.T) {
	svc := addGoSvc()
	out, errStr, code := run(t, svc, `{"findings":[{"path":"add.go","line":999,"body":"x"}]}`, "o/r#7")
	if code != int(core.CodeNotFound) {
		t.Fatalf("exit = %d, want 1 (empty result)", code)
	}
	if svc.postCalls != 0 {
		t.Errorf("nothing commentable must not post, got %d calls", svc.postCalls)
	}
	if errStr != "" {
		t.Errorf("empty result already reported on stdout; stderr must stay clean, got: %s", errStr)
	}
	r := decodeReport(t, out)
	if r.Posted != 0 || len(r.Dropped) != 1 {
		t.Errorf("report = %+v, want posted 0 / 1 dropped", r)
	}
}

func TestSnapFlagReanchors(t *testing.T) {
	svc := addGoSvc()
	out, _, code := run(t, svc, `{"findings":[{"path":"add.go","line":10,"body":"x"}]}`,
		"o/r#7", "--snap", "within:3", "--dry-run")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	r := decodeReport(t, out)
	if len(r.Snapped) != 1 || r.Snapped[0].From != 10 || r.Snapped[0].To != 8 {
		t.Errorf("snapped = %+v, want one {10 -> 8}", r.Snapped)
	}
	if r.Posted != 1 {
		t.Errorf("posted = %d, want 1", r.Posted)
	}
}

func TestFoldDroppedPostsBodyOnly(t *testing.T) {
	svc := addGoSvc()
	svc.url = "u"
	out, _, code := run(t, svc, `{"findings":[{"path":"add.go","line":999,"body":"orphan"}]}`,
		"o/r#7", "--fold-dropped")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (a folded body is still posted)", code)
	}
	if svc.postCalls != 1 {
		t.Fatalf("fold should post a body-only review, got %d calls", svc.postCalls)
	}
	if !strings.Contains(svc.posted.Body, "orphan") {
		t.Errorf("posted body must carry the folded finding: %q", svc.posted.Body)
	}
	r := decodeReport(t, out)
	if r.Posted != 0 || len(r.Folded) != 1 {
		t.Errorf("report = %+v, want posted 0 / 1 folded", r)
	}
}

func TestExitCodesForBadInputs(t *testing.T) {
	cases := []struct {
		name  string
		svc   *fakeSvc
		stdin string
		args  []string
		want  int
	}{
		{"bad target", addGoSvc(), `[]`, []string{"not-a-target"}, int(core.CodeValidation)},
		{"malformed stdin", addGoSvc(), `garbage`, []string{"o/r#7"}, int(core.CodeValidation)},
		{"range in input", addGoSvc(), `[{"path":"add.go","line":6,"body":"x","start_line":3}]`, []string{"o/r#7"}, int(core.CodeValidation)},
		{"bad snap flag", addGoSvc(), `[]`, []string{"o/r#7", "--snap", "sideways"}, int(core.CodeValidation)},
		{"bad event", addGoSvc(), `[]`, []string{"o/r#7", "--event", "PENDING"}, int(core.CodeValidation)},
		{"too many args", addGoSvc(), `[]`, []string{"o/r#7", "o/r#8"}, int(core.CodeValidation)},
		{"files not found", &fakeSvc{filesErr: core.NotFoundf("pr", "no such PR")}, `[{"path":"a","line":1,"body":"b"}]`, []string{"o/r#7"}, int(core.CodeNotFound)},
		{"head sha fails", &fakeSvc{files: []core.File{{Path: "add.go", Patch: patchAdd}}, headErr: core.Internalf("gh", "boom")}, `{"findings":[{"path":"add.go","line":6,"body":"b"}]}`, []string{"o/r#7"}, int(core.CodeInternal)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outStr, errStr, code := run(t, tc.svc, tc.stdin, tc.args...)
			if code != tc.want {
				t.Errorf("exit = %d, want %d (stderr=%s)", code, tc.want, errStr)
			}
			if tc.want == int(core.CodeValidation) && outStr != "" {
				t.Errorf("a hard error must keep stdout clean, got: %s", outStr)
			}
			if tc.svc.postCalls != 0 {
				t.Errorf("a failed run must not post, got %d calls", tc.svc.postCalls)
			}
		})
	}
}

// The JSON funnel must not HTML-escape <, >, & — error messages and the report
// echo diff/path/code content, and stderr envelopes must byte-match the on-disk
// form so `jq` over them stays honest.
func TestRenderErrorNoHTMLEscape(t *testing.T) {
	old := errOut
	var buf bytes.Buffer
	errOut = &buf
	defer func() { errOut = old }()

	renderError(&core.Error{Code: core.CodeValidation, Msg: "line <html> & path a>b"})

	got := buf.String()
	for _, escaped := range []string{"\\u003c", "\\u003e", "\\u0026"} {
		if strings.Contains(got, escaped) {
			t.Errorf("error envelope contains an HTML-escaped sequence (%s): %s", escaped, got)
		}
	}
	if !strings.Contains(got, "line <html> & path a>b") {
		t.Errorf("message not emitted verbatim: %s", got)
	}
}
