# revpost

Pipe findings JSON into **one anchor-verified, batched inline PR review**.
Every comment anchor is checked against the PR diff before posting — snapped
within a bounded window or folded into the review body — so GitHub's
422 `line must be part of the diff` can no longer eat the whole post.

## The pain this kills

- `gh api …/pulls/N/reviews` wants a hand-built comments array
  (path/line/side/…). If one anchor line is not part of a diff hunk, the API
  replies **422 and rejects the entire review** — and agents compute line
  numbers from the checked-out file, so they hit this constantly. Typical cost:
  2-4 turns per review.
- Existing tools don't fit: reviewdog is CI-shaped (env-var setup, lint-input
  model, silent filtering, no snapping); gh-pr-review covers read/reply/resolve
  but posts via a multi-step pending-review dance; the GitHub MCP server does
  multi-call posting with no anchor verification.

## Usage

```console
$ cat findings.json | revpost owner/repo#123 --event COMMENT
{"posted":6,"snapped":[{"path":"src/a.go","from":88,"to":91}],
 "dropped":[{"path":"src/b.go","line":10,"reason":"line not in diff"}],
 "folded":[],"review_url":"https://github.com/…#pullrequestreview-…"}

$ cat findings.json | revpost owner/repo#123 --dry-run              # same report, no post
$ cat findings.json | revpost owner/repo#123 --snap within:3 --fold-dropped
```

Reuses `gh` CLI auth — no extra token setup.

### Input

Findings JSON on stdin — either a top-level object or a bare array:

```jsonc
{
  "body": "optional review summary",
  "findings": [
    { "path": "src/a.go", "line": 88, "body": "markdown comment", "side": "RIGHT" }
  ]
}
```

- `path` (required) — repo-relative file path, exactly as it appears in the PR.
- `line` (required) — 1-based line number: the **new** file for `side: "RIGHT"`
  (the default), the **old** file for `side: "LEFT"`. For a multi-line range this
  is the **last** line.
- `body` (required) — the inline comment, GitHub-flavored markdown. A
  <code>```suggestion</code> block passes through verbatim (multi-line
  suggestions pair with a range).
- `side` (optional) — `RIGHT` (default) or `LEFT`.
- `start_line` (optional) — the **first** line of a multi-line range (`start_line
  <= line`). Both endpoints must be commentable and in the **same diff hunk**, or
  the whole range is dropped/folded — a range is never snapped (which end moves is
  ambiguous). `start_line == line` collapses to a single-line comment.
- `start_side` (optional) — the range's start side; must match `side` (defaults
  to it).
- Unknown fields (e.g. `severity`, `rule`) are ignored, so you can carry your
  own metadata through untouched.

### What happens to each finding

For every finding, revpost checks its anchor against the set of commentable
`(path, line, side)` tuples built from the PR's `/pulls/N/files` patches:

1. **On a commentable line** → posted as-is.
2. **Off the diff, with `--snap within:N`** → moved to the nearest commentable
   line within `N` (ties resolve to the smaller line); the comment is prefixed
   `(re: line <original>)` and recorded under `snapped`. Without `--snap`, it is
   dropped instead.
3. **Off the diff, with `--fold-dropped`** → appended to the review body under a
   "Findings outside the diff" section (recorded under `folded`) — **a finding
   is never lost**.
4. **Otherwise** → recorded under `dropped` with a reason (`line not in diff` vs
   `file not in diff`).

Everything that survives is posted in **one** review request.

### Flags

| Flag | Meaning |
|---|---|
| `--dry-run` | Build and print the same report without posting (`review_url` is `null`). |
| `--snap within:N` | Snap a stray anchor to the nearest commentable line within `N`. Default: drop. |
| `--fold-dropped` | Fold non-commentable findings into the review body instead of dropping them. |
| `--event` | `COMMENT` (default), `REQUEST_CHANGES`, or `APPROVE`. |

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Review posted (or a dry-run that would post). |
| `1` | Empty result — nothing was commentable and nothing folded; no review posted. |
| `2` | Bad usage / validation — fix the args or input, do not retry. |
| `3+` | Internal / IO error (including gh failures other than 404/422). |

stdout carries the report only; diagnostics and the JSON error envelope
(`{"error":{"code","message",…}}`) go to stderr. A malformed batch is rejected
whole (exit 2) — revpost never posts a partial review from broken input.

## Scope

Single-line comments and **multi-line ranges / suggestion blocks** are supported.
Not yet (rejected loudly, never silently downgraded): **rdjsonl input** and an
**idempotency guard** for retries. See [docs/design.md](docs/design.md).

## Install

```sh
go install github.com/akira-toriyama/revpost/cmd/revpost@latest
```

## License

[MIT](LICENSE)
