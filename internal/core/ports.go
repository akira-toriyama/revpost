package core

import "context"

// PRService is the port the CLI depends on for GitHub I/O: fetch a PR's changed
// files, and post one batched review. It is declared here in the domain so the
// CLI depends on this contract, not on the concrete adapter (internal/gh
// implements it, tests substitute a fake). context flows through for cancellation
// on SIGINT/SIGTERM.
type PRService interface {
	Files(ctx context.Context, owner, repo string, number int) ([]File, error)
	// HeadSHA is the PR's current head commit, used to pin the review so a moved
	// head cannot 422 a verified anchor.
	HeadSHA(ctx context.Context, owner, repo string, number int) (sha string, err error)
	// ReviewComments are the PR's existing inline review comments, used by the
	// idempotency guard to skip a comment that was already posted (agents retry).
	ReviewComments(ctx context.Context, owner, repo string, number int) ([]ExistingComment, error)
	PostReview(ctx context.Context, owner, repo string, number int, review Review) (url string, err error)
}
