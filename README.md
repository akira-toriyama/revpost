# revpost

Pipe findings JSON into **one anchor-verified, batched inline PR review**.
Every comment anchor is checked against the PR diff before posting — snapped
within a bounded window or folded into the review body — so GitHub's
422 `line must be part of the diff` can no longer eat the whole post.

> **Status: pre-v0 scaffold.** The house Go CLI spine (exit-code contract,
> stdout/stderr discipline, cobra shell) is in place; the anchor/posting logic
> is not implemented yet. The design lives in [docs/design.md](docs/design.md).

## The pain this kills

- `gh api …/pulls/N/reviews` wants a hand-built comments array
  (path/line/side/start_line/start_side). If one anchor line is not part of a
  diff hunk, the API replies **422 and rejects the entire review** — and agents
  compute line numbers from the checked-out file, so they hit this constantly.
  Typical cost: 2-4 turns per review.
- Existing tools don't fit: reviewdog is CI-shaped (env-var setup, lint-input
  model, silent filtering, no snapping); gh-pr-review covers read/reply/resolve
  but posts via a multi-step pending-review dance; the GitHub MCP server does
  multi-call posting with no anchor verification.

## Planned CLI

```console
$ cat findings.json | revpost owner/repo#123 --event COMMENT
{"posted":6,"snapped":[{"path":"src/a.go","from":88,"to":91}],
 "dropped":[{"path":"src/b.go","line":10,"reason":"line not in diff"}],
 "review_url":"…"}

$ cat findings.json | revpost owner/repo#123 --dry-run          # same report, no post
$ cat findings.json | revpost owner/repo#123 --snap within:3 --fold-dropped
```

A finding is never lost: `--fold-dropped` folds anything outside the diff into
the review body text instead of discarding it.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | OK |
| `1` | not found / empty result |
| `2` | bad usage / validation — fix the args, do not retry |
| `3+` | internal / IO error |

stdout carries pipeable payload only; diagnostics and the JSON error envelope
(`{"error":{"code","message",…}}`) go to stderr.

## Install

From source, for now:

```sh
go install github.com/akira-toriyama/revpost/cmd/revpost@latest
```

Reuses `gh` CLI auth — no extra token setup.

## License

[MIT](LICENSE)
