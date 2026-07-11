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
 "folded":[],"skipped":[],"review_url":"https://github.com/…#pullrequestreview-…"}

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

#### reviewdog input (`--format rdjson` / `--format rdjsonl`)

For cheap interop with the [reviewdog](https://github.com/reviewdog/reviewdog)
ecosystem, revpost also reads rdjson diagnostics — `--format rdjsonl` for one
diagnostic per line, `--format rdjson` for a single `{"diagnostics":[…]}` object:

```console
$ reviewdog -f=golint -diff="git diff" -filter-mode=nofilter | \
    revpost owner/repo#123 --format rdjsonl
```

Each diagnostic maps to a finding: `location.path` → `path`, `message` → `body`,
and the `location.range` lines → the anchor (a range whose `end.line` is past its
`start.line` becomes a multi-line comment; otherwise it is single-line). The side
is always `RIGHT` (diagnostics describe the new file). Everything else the format
carries (`severity`, `source`, `code`, `suggestions`) is ignored. The native
format stays the default; this is purely additive.

### What happens to each finding

For every finding, revpost checks its anchor against the set of commentable
`(path, line, side)` tuples built from the PR's `/pulls/N/files` patches:

1. **On a commentable line** → posted as-is.
2. **Off the diff, with `--snap within:N`** → moved to the nearest commentable
   line within `N` (ties resolve to the smaller line); the comment is prefixed
   `(re: line <original>)` and recorded under `snapped`. Without `--snap` (and
   without `--fold-dropped`), it is dropped instead.
3. **Off the diff, with `--fold-dropped`** → appended to the review body under a
   "Findings outside the diff" section (recorded under `folded`) — **a finding
   is never lost**.
4. **Otherwise** → recorded under `dropped` with a human-readable `reason`
   (e.g. `line not in diff`, `file not in diff`, `file has no commentable lines
   on this side`, or for a range `range spans multiple hunks`).

Everything that survives is posted in **one** review request.

### Idempotency

Agents retry after timeouts, so before posting revpost fetches the PR's existing
inline comments and **skips any it would post that already exists** — an exact
match on anchor (`path`, `side`, `line`, `start_line`) and body. Skipped comments
are listed under `skipped`, and a re-run whose comments are all already posted
does nothing and exits `1` (a clean no-op) rather than double-posting. A first
post skips nothing, so its behavior is unchanged. `--dry-run` reports what would
be skipped without posting.

### Flags

| Flag | Meaning |
|---|---|
| `--dry-run` | Build and print the same report without posting (`review_url` is `null`). |
| `--snap within:N` | Snap a stray anchor to the nearest commentable line within `N`. Default: drop. |
| `--fold-dropped` | Fold non-commentable findings into the review body instead of dropping them. |
| `--event` | `COMMENT` (default), `REQUEST_CHANGES`, or `APPROVE`. |
| `--format` | Input format: `native` (default), `rdjson`, or `rdjsonl` (reviewdog diagnostics). |

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Review posted (or a dry-run that would post). |
| `1` | Soft miss, no review posted — either an **empty result** (nothing commentable or folded and no summary body, **or** every comment was already on the PR; report on stdout, clean stderr) **or the target repo/PR was not found** (gh 404/410; JSON error envelope on stderr, no report). |
| `2` | Bad usage / validation — fix the args or input, do not retry. |
| `3+` | Internal / IO error (including gh failures other than 404/410/422). |

stdout carries the report only; diagnostics and the JSON error envelope
(`{"error":{"code","message",…}}`) go to stderr. A malformed batch is rejected
whole (exit 2) — revpost never posts a partial review from broken input.

## Scope

Single-line comments, **multi-line ranges / suggestion blocks**, **reviewdog
rdjson/rdjsonl input** (`--format`), and an **idempotency guard** (skip comments
already on the PR) are supported. See [docs/design.md](docs/design.md).

## Install

```sh
go install github.com/akira-toriyama/revpost/cmd/revpost@latest
```

## License

[MIT](LICENSE)
