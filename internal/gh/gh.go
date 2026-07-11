// Package gh is revpost's GitHub adapter: it shells out to the `gh` CLI (reusing
// its auth) to fetch a PR's changed files and to post one batched review. It is
// the only impure boundary — the pure verification lives in internal/core. gh's
// HTTP status is translated into the core exit-code contract so callers branch on
// a stable meaning, never on scraped text.
package gh

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/akira-toriyama/revpost/internal/core"
)

// runner executes `gh` with the given args, optionally feeding stdin, and returns
// captured stdout. It is a seam: production uses execGH, tests inject a fake.
type runner func(ctx context.Context, stdin []byte, args ...string) ([]byte, error)

// Client talks to GitHub through the gh CLI.
type Client struct {
	run runner
}

// New returns a Client backed by the real gh binary.
func New() *Client { return &Client{run: execGH} }

// wireFile is the subset of GitHub's /pulls/N/files entry revpost needs. `patch`
// is absent for binary/too-large/pure-rename files and decodes to "".
type wireFile struct {
	Filename string `json:"filename"`
	Patch    string `json:"patch"`
}

// Files fetches every changed file (all pages) for the PR and returns them as
// core.File values for the commentable-set builder.
func (c *Client) Files(ctx context.Context, owner, repo string, number int) ([]core.File, error) {
	endpoint := fmt.Sprintf("repos/%s/%s/pulls/%d/files?per_page=100", owner, repo, number)
	out, err := c.run(ctx, nil, "api", "--paginate", endpoint)
	if err != nil {
		return nil, classify("fetch PR files", err)
	}
	var wire []wireFile
	if err := json.Unmarshal(out, &wire); err != nil {
		return nil, core.Internalf("gh-decode", "could not decode PR files from gh: %v", err)
	}
	files := make([]core.File, len(wire))
	for i, w := range wire {
		files[i] = core.File{Path: w.Filename, Patch: w.Patch}
	}
	return files, nil
}

// wireComment is the subset of a /pulls/N/comments entry the idempotency guard
// needs. line/start_line are absent (null) for an outdated comment whose diff
// moved; they decode to 0, which matches no built comment, so an outdated comment
// simply is not deduped.
type wireComment struct {
	Path      string `json:"path"`
	Side      string `json:"side"`
	Line      int    `json:"line"`
	StartLine int    `json:"start_line"`
	Body      string `json:"body"`
}

// ReviewComments fetches every existing inline review comment (all pages) for the
// PR, for the idempotency guard. An empty side (GitHub omits it for some
// single-line comments) defaults to RIGHT to match how revpost posts.
func (c *Client) ReviewComments(ctx context.Context, owner, repo string, number int) ([]core.ExistingComment, error) {
	endpoint := fmt.Sprintf("repos/%s/%s/pulls/%d/comments?per_page=100", owner, repo, number)
	out, err := c.run(ctx, nil, "api", "--paginate", endpoint)
	if err != nil {
		return nil, classify("fetch PR comments", err)
	}
	var wire []wireComment
	if err := json.Unmarshal(out, &wire); err != nil {
		return nil, core.Internalf("gh-decode", "could not decode PR comments from gh: %v", err)
	}
	comments := make([]core.ExistingComment, len(wire))
	for i, w := range wire {
		side := w.Side
		if side == "" {
			side = core.SideRight
		}
		comments[i] = core.ExistingComment{
			Path: w.Path, Side: side, Line: w.Line, StartLine: w.StartLine, Body: w.Body,
		}
	}
	return comments, nil
}

// HeadSHA returns the PR's current head commit, used to pin the review so a head
// that moves between fetching the diff and posting cannot 422 a verified anchor.
func (c *Client) HeadSHA(ctx context.Context, owner, repo string, number int) (string, error) {
	endpoint := fmt.Sprintf("repos/%s/%s/pulls/%d", owner, repo, number)
	out, err := c.run(ctx, nil, "api", endpoint, "--jq", ".head.sha")
	if err != nil {
		return "", classify("resolve PR head", err)
	}
	sha := strings.TrimSpace(string(out))
	if sha == "" {
		return "", core.Internalf("gh-decode", "gh returned an empty head SHA for %s/%s#%d", owner, repo, number)
	}
	return sha, nil
}

// PostReview posts the batched review in one request (body on stdin via
// `gh api --input -`) and returns the review's html_url.
func (c *Client) PostReview(ctx context.Context, owner, repo string, number int, review core.Review) (string, error) {
	payload, err := marshalReview(review)
	if err != nil {
		return "", core.Internalf("gh-encode", "could not encode review payload: %v", err)
	}
	endpoint := fmt.Sprintf("repos/%s/%s/pulls/%d/reviews", owner, repo, number)
	out, err := c.run(ctx, payload, "api", "--method", "POST", endpoint, "--input", "-")
	if err != nil {
		return "", classify("post review", err)
	}
	var resp struct {
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return "", core.Internalf("gh-decode", "could not decode review response from gh: %v", err)
	}
	return resp.HTMLURL, nil
}

// marshalReview encodes the review with HTML escaping off so <, >, & in code and
// diff content reach GitHub verbatim.
func marshalReview(review core.Review) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(review); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// execError carries the child's stderr alongside the exec failure so the status
// can be classified and the operator sees the one diagnostic line that matters.
type execError struct {
	err    error
	stderr string
}

func (e *execError) Error() string {
	if line := distill(e.stderr); line != "" {
		return line
	}
	return e.err.Error()
}

func (e *execError) Unwrap() error { return e.err }

func execGH(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	// #nosec G204 -- args are fixed gh verbs plus an endpoint built from a
	// validated owner/repo/number; the JSON payload travels on stdin, not argv.
	cmd := exec.CommandContext(ctx, "gh", args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), &execError{err: err, stderr: stderr.String()}
	}
	return stdout.Bytes(), nil
}

var httpStatus = regexp.MustCompile(`\(HTTP (\d{3})\)`)

// classify maps a gh failure to the exit-code contract: a missing binary and any
// non-404/422 status are internal; 404/410 is not-found; 422 is validation
// (revpost's anchor check should prevent 422, so a residual one is worth seeing).
func classify(op string, err error) error {
	var ee *execError
	if !errors.As(err, &ee) {
		return core.Internalf("gh", "%s: %v", op, err)
	}
	if errors.Is(ee.err, exec.ErrNotFound) {
		return core.Internalf("gh-missing", "the gh CLI is required but was not found on PATH")
	}
	code := core.CodeInternal
	if m := httpStatus.FindStringSubmatch(ee.stderr); m != nil {
		switch m[1] {
		case "404", "410":
			code = core.CodeNotFound
		case "422":
			code = core.CodeValidation
		}
	}
	return &core.Error{Code: code, ID: "gh-api", Msg: fmt.Sprintf("%s: %s", op, ee.Error())}
}

// distill picks the most diagnostic single line from gh's stderr: the one gh
// prints its error on ("gh: … (HTTP NNN)"), else the last non-empty line.
func distill(stderr string) string {
	var last string
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "gh:") || httpStatus.MatchString(line) {
			return line
		}
		last = line
	}
	return last
}
