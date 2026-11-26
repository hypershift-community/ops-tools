package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

type flowOptions struct {
	scan    Config
	proof   proofOptions
	verbose bool
}

func newFlowCommand() *cobra.Command {
	scan := DefaultConfig()
	cfg := flowOptions{scan: scan, proof: defaultProofOptions(scan.OutputDir)}
	cmd := &cobra.Command{
		Use:   "flow",
		Short: "Scan AWS, look up Prow proof, and save an HTML report without deleting anything",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runFlow(cmd.Context(), cfg, cleanleakedInvocation(), cmd.OutOrStdout(), scanForReport, proveScan)
		},
	}
	cmd.Flags().StringVar(&cfg.scan.Region, "region", cfg.scan.Region, "AWS region")
	cmd.Flags().DurationVar(&cfg.scan.MinAge, "min-age", cfg.scan.MinAge, "Minimum VPC age to consider for deletion")
	cmd.Flags().StringVar(&cfg.scan.Target, "target", "", "Scan a single infra ID or VPC ID instead of all VPCs")
	cmd.Flags().StringVar(&cfg.scan.OutputDir, "output-dir", cfg.scan.OutputDir, "Parent directory for this run's scan, proof, and HTML reports")
	cmd.Flags().BoolVarP(&cfg.verbose, "verbose", "v", false, "Show detailed AWS scan progress, including each leaked infra set")
	bindProwFlags(cmd, &cfg.proof)
	return cmd
}

func runFlow(ctx context.Context, cfg flowOptions, invocation []string, out io.Writer,
	scan func(context.Context, Config, io.Writer) (Report, error), prove func(context.Context, Report, proofOptions) (Report, error),
) error {
	if err := cfg.proof.validate(); err != nil {
		return err
	}
	if cfg.scan.Region == "" || cfg.scan.MinAge < 0 || cfg.scan.OutputDir == "" {
		return fmt.Errorf("flow requires a region, a non-negative minimum age, and an output directory")
	}
	parent, err := filepath.Abs(cfg.scan.OutputDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(parent, 0755); err != nil {
		return fmt.Errorf("create flow output directory: %w", err)
	}
	dir, err := os.MkdirTemp(parent, "flow-"+time.Now().UTC().Format("20060102T150405Z")+"-")
	if err != nil {
		return fmt.Errorf("create flow run directory: %w", err)
	}
	_, _ = fmt.Fprintf(out, "[1/3] Scan AWS inventory · %s\n", cfg.scan.Region)
	scanProgress := io.Discard
	if cfg.verbose {
		scanProgress = out
	}
	scanReport, err := scan(ctx, cfg.scan, scanProgress)
	if err != nil {
		return err
	}
	if err := saveFlowJSON(filepath.Join(dir, "scan.json"), scanReport); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, "[2/3] Look up Prow evidence")
	proofReport, err := prove(ctx, scanReport, cfg.proof)
	if err != nil {
		return fmt.Errorf("look up Prow proof (scan saved in %s): %w", dir, err)
	}
	proofPath := filepath.Join(dir, "proofs.json")
	if err := saveFlowJSON(proofPath, proofReport); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, "[3/3] Generate HTML report")
	htmlPath := filepath.Join(dir, "report.html")
	if err := writeHTMLFile(htmlPath, scanReport, proofReport); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return printFlowNextSteps(out, cfg.scan, scanReport, proofReport, htmlPath, proofPath, invocation)
}

func saveFlowJSON(path string, report Report) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if err := errors.Join(PrintJSON(f, report), f.Close()); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func printFlowNextSteps(out io.Writer, cfg Config, scan, proof Report, htmlPath, proofPath string, invocation []string) error {
	index, err := indexProofs(proof)
	if err != nil {
		return err
	}
	eligible, skipped := selectDeletable(slices.Clone(scan.InfraSets), index, true)
	var text strings.Builder
	text.WriteString("\nInvestigation complete · No AWS resources deleted\n\n")
	fmt.Fprintf(&text, "  %-20s  %s\n", "Region", cfg.Region)
	fmt.Fprintf(&text, "  %-20s  %d\n", "Leak candidates", len(eligible)+skipped)
	fmt.Fprintf(&text, "  %-20s  %d\n", "Eligible for cleanup", len(eligible))
	fmt.Fprintf(&text, "  %-20s  %d\n", "Insufficient proof", skipped)
	fmt.Fprintf(&text, "  %-20s  %s\n", "HTML report", shortFlowPath(htmlPath))
	if len(eligible) == 0 {
		text.WriteString("\nNo cleanup command is suggested.\n")
		_, err := io.WriteString(out, text.String())
		return err
	}
	lines := []string{
		shellCommand(append(slices.Clone(invocation), "delete", "--interactive", "--require-proof=true")),
		shellCommand([]string{"--region", cfg.Region, "--min-age", cfg.MinAge.String()}),
		shellCommand([]string{"--proof", shortFlowPath(proofPath)}),
		shellCommand([]string{"--output-dir", shortFlowPath(filepath.Dir(proofPath))}),
	}
	if cfg.Target != "" {
		lines = append(lines, shellCommand([]string{"--target", cfg.Target}))
	}
	fmt.Fprintf(&text, "\nNext step\n  Review the HTML report. If the findings look right, run this from the same directory.\n  Cleanup asks before deleting each infra set and its associated resources.\n\n  %s\n", strings.Join(lines, " \\\n    "))
	_, err = io.WriteString(out, text.String())
	return err
}

func shortFlowPath(path string) string {
	cwd, err := os.Getwd()
	if err != nil {
		return path
	}
	rel, err := filepath.Rel(cwd, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	return rel
}

func cleanleakedInvocation() []string {
	executable, err := os.Executable()
	if err != nil {
		return []string{"cleanleaked"}
	}
	// `go run .` removes its temporary executable when it exits.
	if strings.HasPrefix(filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(executable)))), "go-build") {
		return []string{"go", "run", "."}
	}
	if rel := shortFlowPath(executable); !filepath.IsAbs(rel) {
		return []string{"./" + rel}
	}
	return []string{executable}
}

func shellCommand(args []string) string {
	const unquoted = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_./:=+-"
	quoted := make([]string, len(args))
	for i, arg := range args {
		if arg == "" || strings.ContainsFunc(arg, func(r rune) bool {
			return !strings.ContainsRune(unquoted, r)
		}) {
			arg = "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
		}
		quoted[i] = arg
	}
	return strings.Join(quoted, " ")
}
