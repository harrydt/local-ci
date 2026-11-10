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
	"path/filepath"
	"strings"
	"syscall"

	"github.com/teeplabs/localci/internal/github"
	"github.com/teeplabs/localci/internal/runner"
	"github.com/teeplabs/localci/scripts"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: localci [flags] <command>

commands:
  setup     create the Lima VM and install the runner in it; safe to re-run
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
	case "setup":
		return setup(fs.Args()[1:], stderr)
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
	label := fs.String("label", "", "runner label (default localci-<your login>)")
	vm := fs.String("vm", "localci", "Lima instance name")
	runners := fs.Int("runners", 1, "jobs to run in parallel, each on its own runner in the VM")
	if err := fs.Parse(args); err != nil {
		return err
	}
	owner, name, ok := strings.Cut(*repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return fmt.Errorf("-repo must be owner/name, got %q", *repo)
	}
	if *runners < 1 {
		return fmt.Errorf("-runners must be at least 1, got %d", *runners)
	}

	// SIGHUP too: closing the terminal must unroute, or jobs queue for a dead runner.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	token, err := githubToken(ctx)
	if err != nil {
		return err
	}
	gh := github.New(token)
	login, err := gh.Login(ctx)
	if err != nil {
		return fmt.Errorf("look up your GitHub login: %w", err)
	}
	if *label == "" {
		*label = "localci-" + login
	}
	agent := &runner.Agent{
		GitHub: gh,
		Owner:  owner, Repo: name, Login: login, Label: *label, VM: *vm, Runners: *runners,
		Log: logger.With("repo", *repo, "label", *label),
	}
	logger.Info("localci up", "repo", *repo, "label", *label, "vm", *vm, "runners", *runners)
	return agent.Run(ctx)
}

func setup(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	vm := fs.String("vm", "localci", "Lima instance name")
	useCache := fs.Bool("cache", true, "serve actions/cache from the VM's disk instead of GitHub (needs localci-cache-linux-arm64 next to localci)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cacheState, cacheBin := "off", ""
	if *useCache {
		cacheState = "on"
		if exe, err := os.Executable(); err == nil {
			cacheBin = filepath.Join(filepath.Dir(exe), "localci-cache-linux-arm64")
		}
	}
	cmd := exec.CommandContext(ctx, "bash", "-c", scripts.SetupVM, "setup-vm.sh")
	cmd.Env = append(os.Environ(), "VM="+*vm, "CACHE="+cacheState, "CACHE_BIN="+cacheBin, "INSTALL_CACHE="+scripts.InstallCache)
	cmd.Stdout, cmd.Stderr = stderr, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("setup VM %q: %w", *vm, err)
	}
	return nil
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
