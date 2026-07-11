# revpost — design

> Distilled from tracker task `t-a01h` (private board, 2026-07-03 research,
> refined 2026-07-06). This is the pre-implementation design; verify premises
> (API behavior, prior-art state) before building on them. revpost is the
> write-side twin of agynio/gh-pr-review (which owns read/reply/resolve).

## Status (v1 shipped)

The core pipeline below is implemented: stdin findings (object or bare array) →
commentable `(path,line,side)` set from `/pulls/N/files` → keep / snap
(`--snap within:N`) / drop / `--fold-dropped` → one batched POST →
`posted/snapped/dropped/folded/review_url` report. `--dry-run` and `--event`
are wired. Layers: pure `internal/core` (findings, diff, plan, args), the
`internal/gh` adapter (reuses `gh` auth), the `internal/cli` cobra shell.

Verified against the GitHub REST API: `line`/`side` model (RIGHT=new file,
LEFT=old, default RIGHT); `gh api --paginate` merges array pages; a COMMENT
review with inline comments posts with the top-level body omitted.

**Shipped since v1**:
- multi-line ranges + suggestion blocks (design note 3) — `start_line`/`start_side`
  are verified so both endpoints sit in the same hunk (GitHub 422s a straddling
  range), ranges never snap (which end moves is ambiguous), and `` ```suggestion ``
  bodies pass through verbatim.
- reviewdog rdjson/rdjsonl input (design note 4) — `--format rdjson|rdjsonl` maps
  each diagnostic (`location.path`/`location.range`/`message`) onto a finding and
  reuses the native validation; the native format stays the default. Suggestions
  carried by a diagnostic are not yet translated into ` ```suggestion ` blocks.

**Deferred** (rejected loudly, never silently downgraded — has a follow-up task):
the idempotency guard (note 6).

## What

Read findings JSON on stdin, verify every anchor against the set of
commentable `(path, line, side)` tuples built from `/pulls/N/files` patches,
snap-or-drop, then POST **one** batched inline review. Return a
machine-readable `posted / snapped / dropped` report.

## Pain

- The comments array for `gh api …/pulls/N/reviews` is hand-built
  (path/line/side/start_line/start_side). An anchor outside a diff hunk →
  **422 "line must be part of the diff"** — and one bad line rejects the
  entire review. Agents compute line numbers from the checked-out file, so
  they hit this habitually; typical cost 2-4 turns.
- Honest note (verified): shell-escaping is already solved by `--input
  body.json`, and the batch endpoint exists. **The unsolved core is anchor
  verification alone** — the tool goes all-in on that.

## Design notes (from the verification agent's refinement)

1. Snap is **opt-in and bounded** (`--snap within:3` vs `--drop`): silently
   moving a comment misattributes the finding. When snapped, prefix the body
   with "(re: line 88)" and always report from/to.
2. `--fold-dropped`: findings outside the diff fold into the review body text —
   **a finding is never lost** (the agent-side analogue of reviewdog's
   check-annotation fallback, which only works inside Actions).
3. Support multi-line ranges (start_line/line) and GitHub suggestion blocks;
   verify both ends sit in the same hunk — ranges are the worst breakage point
   of hand-built payloads since agents post fixes.
4. Accept rdjsonl as an alternate input (cheap interop with the reviewdog
   ecosystem).
5. read/reply/resolve are **out of scope** — gh-pr-review owns them; the
   read/write pairing is the right division.
6. `--dry-run` (same posted/snapped/dropped report, no post) and an idempotency
   guard (skip comments already on the PR — agents retry after timeouts).
7. Reuse `gh` auth; positional `owner/repo#123` — zero env-var setup is the
   ergonomic answer to reviewdog.

## Prior art

- reviewdog: ~70% of the machinery (stdin diagnostics → diff filter → batch
  post) but CI-shaped: env-var config, lint-diagnostic input model, silent
  filtering, no snapping.
- agynio/gh-pr-review: posting is a multi-step pending-review dance
  (start → per-comment → submit), not a one-shot batch.
- GitHub MCP server: multi-call, verbose, no anchor verification.

## Refs

- https://github.com/reviewdog/reviewdog
- https://github.com/agynio/gh-pr-review
