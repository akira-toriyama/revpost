// Package cli is the cobra adapter — revpost's command-line presentation
// layer. It parses flags, will call the pure core for every operation, and
// renders the result. It holds no domain logic. stdout carries pipeable
// payload only; diagnostics and error envelopes go to stderr.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/akira-toriyama/revpost/internal/core"
	"github.com/akira-toriyama/revpost/internal/version"
)

// out/errOut are the single funnel for process output: stdout = payload,
// stderr = diagnostics. No other file writes to os.Stdout/os.Stderr directly.
var (
	out    io.Writer = os.Stdout
	errOut io.Writer = os.Stderr
)

// Execute builds the root command, runs it, and maps the result to the
// exit-code contract: 0 ok / 1 not-found|empty / 2 bad-usage|validation /
// 3+ internal|IO. On a non-zero exit it prints {"error":{...}} to stderr. It
// is the only place that decides the process exit code; main is just
// os.Exit(cli.Execute()).
//
// Signals: the root context cancels on the first SIGINT/SIGTERM so in-flight
// API calls can unwind gracefully; the deferred stop restores the default
// disposition, so a second Ctrl-C hard-kills a wedged process.
func Execute() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop() // restore default disposition: a second Ctrl-C terminates hard
	}()

	root := newRootCmd()
	err := root.ExecuteContext(ctx)
	if err == nil {
		return int(core.CodeOK)
	}
	// Core code always returns *core.Error; a bare error here can only be a
	// cobra flag/arg parse problem, which is a usage error by contract.
	ce := core.AsError(err)
	if ce == nil {
		ce = &core.Error{Code: core.CodeValidation, Msg: err.Error()}
	}
	renderError(ce)
	return int(ce.Code)
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "revpost <owner/repo#N>",
		Short: "Pipe findings JSON into one anchor-verified batched inline PR review (kills the 422 loop)",
		Long: "revpost reads findings JSON on stdin, verifies every comment anchor against\n" +
			"the PR diff's commentable lines (snap within a bounded window, or drop into\n" +
			"the review body — a finding is never lost), and posts ONE batched inline\n" +
			"review. The API's 422 \"line must be part of the diff\" can no longer eat the\n" +
			"whole post, and the machine-readable report says exactly what happened.\n\n" +
			"Planned grammar (design: docs/design.md — not implemented yet):\n" +
			"  cat findings.json | revpost owner/repo#123 --event COMMENT\n" +
			"  cat findings.json | revpost owner/repo#123 --dry-run   # same report, no post\n" +
			"  cat findings.json | revpost owner/repo#123 --snap within:3 --fold-dropped",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version.Resolve().String(),
		// Pre-v0: a real invocation fails loudly instead of silently printing
		// help — an agent must not mistake a no-op for success. Bare `revpost`
		// still shows help.
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			return core.Internalf("not-implemented",
				"revpost is a pre-v0 scaffold — nothing is implemented yet (see docs/design.md)")
		},
	}
	root.SetOut(out)
	root.SetErr(errOut)
	return root
}

// renderError prints the structured error envelope to stderr — never stdout,
// so piping the payload into jq stays clean.
func renderError(e *core.Error) {
	env := map[string]any{"code": int(e.Code), "message": e.Msg}
	if e.ID != "" {
		env["id"] = e.ID
	}
	if e.Details != nil {
		env["details"] = e.Details
	}
	b, err := json.Marshal(map[string]any{"error": env})
	if err != nil {
		fmt.Fprintln(errOut, e.Msg)
		return
	}
	fmt.Fprintln(errOut, string(b))
}
