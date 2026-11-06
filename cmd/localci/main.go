// Command localci runs GitHub Actions jobs on the developer's machine via an
// ephemeral self-hosted runner inside a local Linux VM.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/teeplabs/localci/internal/github"
	"github.com/teeplabs/localci/internal/runner"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: localci [flags] <command>

commands:
  up        run GitHub Actions jobs routed to this machine (localci up -h)
  version   print the localci version

flags:
`

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "localci:", err)
		os.Exit(2)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("localci", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, usage)
		fs.PrintDefaults()
	}
	logLevel := fs.String("log-level", envOr("LOCALCI_LOG_LEVEL", "info"), "log level: debug, info, warn, error (env LOCALCI_LOG_LEVEL)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var level slog.Level
	if err := level.UnmarshalText([]byte(*logLevel)); err != nil {
		return fmt.Errorf("invalid -log-level %q", *logLevel)
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))

	switch fs.Arg(0) {
	case "version":
		fmt.Fprintln(stdout, version)
		return nil
	case "up":
		return up(fs.Args()[1:], stderr, logger)
	case "":
		fs.Usage()
		return errors.New("missing command")
	default:
		logger.Debug("unknown command", "command", fs.Arg(0))
		fs.Usage()
		return fmt.Errorf("unknown command %q", fs.Arg(0))
	}
}

func up(args []string, stderr io.Writer, logger *slog.Logger) error {
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", "", "GitHub repository as owner/name (required)")
	label := fs.String("label", "", "runner label, must match this user's entry in LOCALCI_RUNNERS (required)")
	vm := fs.String("vm", "localci", "Lima instance name")
	runners := fs.Int("runners", 1, "jobs to run in parallel, each on its own runner in the VM")
	if err := fs.Parse(args); err != nil {
		return err
	}
	owner, name, ok := strings.Cut(*repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return fmt.Errorf("-repo must be owner/name, got %q", *repo)
	}
	if *label == "" {
		return errors.New("-label is required")
	}
	if *runners < 1 {
		return fmt.Errorf("-runners must be at least 1, got %d", *runners)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	token, err := githubToken(ctx)
	if err != nil {
		return err
	}
	agent := &runner.Agent{
		GitHub: github.New(token),
		Owner:  owner, Repo: name, Label: *label, VM: *vm, Runners: *runners,
		Log: logger.With("repo", *repo, "label", *label),
	}
	logger.Info("localci up", "repo", *repo, "label", *label, "vm", *vm, "runners", *runners)
	return agent.Run(ctx)
}

// githubToken prefers GH_TOKEN and falls back to the gh CLI. The token is
// held in memory only.
func githubToken(ctx context.Context) (string, error) {
	if t := os.Getenv("GH_TOKEN"); t != "" {
		return t, nil
	}
	out, err := exec.CommandContext(ctx, "gh", "auth", "token").Output()
	if err != nil {
		return "", fmt.Errorf("no GH_TOKEN and `gh auth token` failed (run `gh auth login`): %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
