// Package cli is the cobra adapter — revpost's command-line presentation layer.
// It parses flags, reads the findings on stdin, calls the pure core to verify
// anchors and build the review, calls the GitHub port to post it, and renders the
// machine-readable report. It holds no domain logic. stdout carries the report
// only; diagnostics and error envelopes go to stderr.
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
	"github.com/akira-toriyama/revpost/internal/gh"
	"github.com/akira-toriyama/revpost/internal/version"
)

// in/out/errOut are the single funnels for process I/O: stdin = findings,
// stdout = report payload, stderr = diagnostics. Nothing writes to the os
// streams directly, so tests can substitute buffers.
var (
	in     io.Reader = os.Stdin
	out    io.Writer = os.Stdout
	errOut io.Writer = os.Stderr
)

// Execute wires the real GitHub adapter and runs the command, returning the
// process exit code.
//
// Signals: the root context cancels on the first SIGINT/SIGTERM so in-flight API
// calls unwind gracefully; the deferred stop restores the default disposition, so
// a second Ctrl-C hard-kills a wedged process.
func Execute() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop() // restore default disposition: a second Ctrl-C terminates hard
	}()

	return runWith(ctx, gh.New(), os.Args[1:])
}

// runWith runs the command against a given PRService and args, and maps the
// result to the exit-code contract: 0 ok / 1 not-found|empty / 2
// bad-usage|validation / 3+ internal|IO. It is the only place that decides the
// exit code. A Silent error already reported its outcome on stdout, so its stderr
// envelope is suppressed.
func runWith(ctx context.Context, svc core.PRService, args []string) int {
	root := newRootCmd(svc)
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return int(core.CodeOK)
	}
	// Core/adapter code always returns *core.Error; a bare error here can only be
	// a cobra flag/arg parse problem, which is a usage error by contract.
	ce := core.AsError(err)
	if ce == nil {
		ce = &core.Error{Code: core.CodeValidation, Msg: err.Error()}
	}
	if !ce.Silent {
		renderError(ce)
	}
	return int(ce.Code)
}

func newRootCmd(svc core.PRService) *cobra.Command {
	var (
		dryRun      bool
		foldDropped bool
		snapFlag    string
		eventFlag   string
	)
	root := &cobra.Command{
		Use:   "revpost <owner/repo#N>",
		Short: "Pipe findings JSON into one anchor-verified batched inline PR review (kills the 422 loop)",
		Long: "revpost reads findings JSON on stdin, verifies every comment anchor against\n" +
			"the PR diff's commentable lines (snap within a bounded window, or drop into\n" +
			"the review body — a finding is never lost), and posts ONE batched review. The\n" +
			"API's 422 \"line must be part of the diff\" can no longer eat the whole post, and\n" +
			"the machine-readable report says exactly what happened.\n\n" +
			"  cat findings.json | revpost owner/repo#123 --event COMMENT\n" +
			"  cat findings.json | revpost owner/repo#123 --dry-run              # same report, no post\n" +
			"  cat findings.json | revpost owner/repo#123 --snap within:3 --fold-dropped\n\n" +
			"Findings JSON is an object {\"body\"?, \"findings\":[…]} or a bare array; each\n" +
			"finding is {\"path\",\"line\",\"body\",\"side\"?} (side RIGHT|LEFT, default RIGHT).",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version.Resolve().String(),
		Args:          cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				// A bare invocation is a usage error, not a report — keep stdout
				// clean (guidance goes to stderr). `--help` still prints full help.
				return core.Validationf("no-target",
					"expected a target: revpost <owner/repo#N> (run 'revpost --help' for usage)")
			}
			opts := runOpts{
				dryRun:      dryRun,
				foldDropped: foldDropped,
				snap:        snapFlag,
				event:       eventFlag,
			}
			return runReview(cmd.Context(), svc, args, opts)
		},
	}
	root.Flags().BoolVar(&dryRun, "dry-run", false, "build and print the report without posting")
	root.Flags().BoolVar(&foldDropped, "fold-dropped", false, "fold non-commentable findings into the review body instead of dropping them")
	root.Flags().StringVar(&snapFlag, "snap", "", "snap stray anchors to the nearest commentable line: within:N (default: drop)")
	root.Flags().StringVar(&eventFlag, "event", "COMMENT", "review event: COMMENT | REQUEST_CHANGES | APPROVE")
	root.SetOut(out)
	root.SetErr(errOut)
	return root
}

type runOpts struct {
	dryRun      bool
	foldDropped bool
	snap        string
	event       string
}

// runReview is the pipeline: validate → read findings → fetch diff → verify →
// (post) → report. It returns a typed *core.Error for every failure so the exit
// code is decided by contract, never re-derived from strings.
func runReview(ctx context.Context, svc core.PRService, args []string, opts runOpts) error {
	if len(args) != 1 {
		return core.Validationf("bad-usage", "expected exactly one target owner/repo#N, got %d arguments", len(args))
	}
	owner, repo, number, err := core.ParseTarget(args[0])
	if err != nil {
		return err
	}
	within, err := core.ParseSnapWithin(opts.snap)
	if err != nil {
		return err
	}
	event, err := core.ValidateEvent(opts.event)
	if err != nil {
		return err
	}

	data, err := readStdin()
	if err != nil {
		return err
	}
	input, err := core.ParseInput(data)
	if err != nil {
		return err
	}

	files, err := svc.Files(ctx, owner, repo, number)
	if err != nil {
		return err
	}

	plan := core.BuildPlan(input, core.BuildCommentSet(files), core.Options{
		SnapWithin:  within,
		FoldDropped: opts.foldDropped,
		Event:       event,
	})

	// Something to post = at least one inline comment, or a review body (an
	// operator summary and/or folded findings). Otherwise there is nothing to say.
	somethingToPost := len(plan.Review.Comments) > 0 || plan.Review.Body != ""

	var url *string
	if !opts.dryRun && somethingToPost {
		// Pin the review to the head the diff was just computed from, so a push in
		// this window can't turn a verified anchor into a 422.
		sha, err := svc.HeadSHA(ctx, owner, repo, number)
		if err != nil {
			return err
		}
		plan.Review.CommitID = sha
		u, err := svc.PostReview(ctx, owner, repo, number, plan.Review)
		if err != nil {
			return err // stdout stays clean; the report would misreport a post that failed
		}
		url = &u
	}

	if err := writeJSON(out, plan.Report(url)); err != nil {
		return core.Internalf("write", "could not write report: %v", err)
	}
	if !somethingToPost {
		// Report already printed; exit 1 (empty result) without a stderr envelope.
		return &core.Error{Code: core.CodeNotFound, Silent: true, Msg: "no commentable findings to post"}
	}
	return nil
}

// readStdin reads the whole findings payload. Reading directly from a terminal
// would hang waiting for input the operator did not pipe, so that case fails fast
// with guidance.
func readStdin() ([]byte, error) {
	if f, ok := in.(*os.File); ok && isTerminal(f) {
		return nil, core.Validationf("no-input", "findings JSON expected on stdin — pipe it in (e.g. cat findings.json | revpost …)")
	}
	data, err := io.ReadAll(in)
	if err != nil {
		return nil, core.Internalf("read", "could not read findings from stdin: %v", err)
	}
	return data, nil
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// renderError prints the structured error envelope to stderr — never stdout, so
// piping the report into jq stays clean.
func renderError(e *core.Error) {
	env := map[string]any{"code": int(e.Code), "message": e.Msg}
	if e.ID != "" {
		env["id"] = e.ID
	}
	if e.Details != nil {
		env["details"] = e.Details
	}
	if err := writeJSON(errOut, map[string]any{"error": env}); err != nil {
		fmt.Fprintln(errOut, e.Msg)
	}
}

// writeJSON is the single JSON funnel for the report and error envelopes: HTML
// escaping is off so <, >, & survive verbatim (content echoes diff/path/code) and
// the emitted bytes match the on-disk encoding. Encode appends a trailing newline.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}
