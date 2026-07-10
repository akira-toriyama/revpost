// revpost turns a findings JSON stream into one batched inline pull-request
// review: every comment anchor is verified against the PR diff first (snapped
// within a bounded window, or dropped into the review body), so the API's 422
// "line must be part of the diff" can no longer eat the whole post. main is
// the only untestable process boundary — everything below returns and is
// exercised by tests through cli.Execute.
package main

import (
	"os"

	"github.com/akira-toriyama/revpost/internal/cli"
)

func main() { os.Exit(cli.Execute()) }
