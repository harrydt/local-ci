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
	"maps"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/harrydt/local-ci/internal/github"
	"github.com/harrydt/local-ci/internal/runner"
	"github.com/harrydt/local-ci/internal/workflow"
	"github.com/harrydt/local-ci/scripts"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: localci [flags] <command>

commands:
  setup     create the Lima VM and install the runner in it; safe to re-run
  auth      store a GitHub token for one repo in the macOS Keychain (localci auth -h)
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
	case "auth":
		return auth(fs.Args()[1:], stderr)
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
	owner, name, err := splitRepo(*repo)
	if err != nil {
		return err
	}
	if *runners < 1 {
		return fmt.Errorf("-runners must be at least 1, got %d", *runners)
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	release, err := lockVM(filepath.Join(cache, "localci"), *vm)
	if err != nil {
		return err
	}
	defer release()

	// SIGHUP too: closing the terminal must unroute, or jobs queue for a dead runner.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	token, err := githubToken(ctx, *repo, logger)
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
	warnRoutedSecrets(ctx, gh, owner, name, logger)
	warnWritableToken(ctx, gh, owner, name, logger)
	agent := &runner.Agent{
		GitHub: gh,
		Owner:  owner, Repo: name, Login: login, Label: *label, VM: *vm, Runners: *runners,
		Log: logger.With("repo", *repo, "label", *label),
	}
	logger.Info("localci up", "repo", *repo, "label", *label, "vm", *vm, "runners", *runners)
	return agent.Run(ctx)
}

// warnRoutedSecrets warns about jobs that opt in to LocalCI but use secrets, an
// environment or OIDC, so they would run on this Mac (docs/security.md). It
// checks the default branch only and never stops localci up.
func warnRoutedSecrets(ctx context.Context, gh *github.Client, owner, repo string, logger *slog.Logger) {
	files, err := gh.Workflows(ctx, owner, repo)
	if err != nil {
		logger.Warn("could not check workflows for secrets", "err", err)
		return
	}
	for _, file := range slices.Sorted(maps.Keys(files)) {
		for _, f := range workflow.Check(files[file]) {
			logger.Warn("job uses "+f.What+" but runs on this Mac; keep it on hosted runners", "workflow", file, "job", f.Job)
		}
	}
}

// warnWritableToken warns when jobs get a read-write GITHUB_TOKEN by default:
// job code on this Mac, a dependency included, could then push to the repo.
func warnWritableToken(ctx context.Context, gh *github.Client, owner, repo string, logger *slog.Logger) {
	perm, err := gh.DefaultWorkflowPermissions(ctx, owner, repo)
	if err != nil {
		logger.Warn("could not check default GITHUB_TOKEN permissions", "err", err)
		return
	}
	if perm == "write" {
		logger.Warn("jobs get a read-write GITHUB_TOKEN by default; set it to read in Settings > Actions > General > Workflow permissions, and grant more per job with permissions:")
	}
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
			// Homebrew links bin/localci to libexec/localci; the cache binary is next to the target.
			if real, err := filepath.EvalSymlinks(exe); err == nil {
				exe = real
			}
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

func splitRepo(repo string) (owner, name string, err error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", "", fmt.Errorf("-repo must be owner/name, got %q", repo)
	}
	return owner, name, nil
}

// lockVM makes sure only one localci up uses a VM: two would share runner
// directories and kill each other's runners. The OS drops the lock when the
// process exits, even on SIGKILL.
func lockVM(dir, vm string) (release func(), err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, vm+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another localci up is using VM %q; stop it first, or use another VM (localci setup -vm name, then localci up -vm name)", vm)
	}
	return func() { f.Close() }, nil
}

// keychainService names localci's Keychain items. The account is owner/name.
const keychainService = "localci"

// auth stores a fine-grained token for one repo in the Keychain.
// `security` prompts for the token itself, so it never passes through localci's
// arguments, output or memory until up reads it back.
func auth(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("auth", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", "", "GitHub repository as owner/name (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	owner, name, err := splitRepo(*repo)
	if err != nil {
		return err
	}
	// Without a terminal, security reads end-of-input and stores an empty token.
	tty, err := os.Open("/dev/tty")
	if err != nil {
		return fmt.Errorf("localci auth asks for the token, so run it in a terminal: %w", err)
	}
	defer tty.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	link := tokenURL(*repo)
	fmt.Fprintf(stderr, "Create a token at %s\nUnder Repository access, pick \"Only select repositories\" and choose %s.\nCheck the permissions: Administration read and write, Variables read and write, Contents read-only. Then paste it here.\n", link, name)
	_ = exec.CommandContext(ctx, "open", link).Run()
	cmd := exec.CommandContext(ctx, "security", "add-generic-password", "-U", "-s", keychainService, "-a", *repo, "-w")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, stderr, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("store token in the Keychain: %w", err)
	}

	// A token that can't read the repo's Actions settings is removed, so up
	// never picks it over the gh fallback.
	token, err := keychainToken(ctx, *repo)
	if err == nil && token == "" {
		err = errors.New("empty token")
	}
	if err == nil {
		_, err = github.New(token).DefaultWorkflowPermissions(ctx, owner, name)
	}
	if err != nil {
		_ = exec.Command("security", "delete-generic-password", "-s", keychainService, "-a", *repo).Run()
		return fmt.Errorf("token not stored: it can't read %s's Actions settings; check it has %s selected and Administration access, then run localci auth again: %w", *repo, name, err)
	}
	fmt.Fprintf(stderr, "Token stored. localci up -repo %s will use it.\n", *repo)
	return nil
}

// tokenURL pre-fills GitHub's fine-grained token form with what localci up
// needs: runners (Administration), LOCALCI_RUNNERS (Variables) and the workflow
// scan (Contents). GitHub can't pre-select the repo.
func tokenURL(repo string) string {
	owner, _, _ := strings.Cut(repo, "/")
	q := url.Values{
		"name":              {cut("localci "+repo, 40)},
		"description":       {"localci up for " + repo},
		"target_name":       {owner},
		"expires_in":        {"90"},
		"administration":    {"write"},
		"actions_variables": {"write"},
		"contents":          {"read"},
	}
	return "https://github.com/settings/personal-access-tokens/new?" + q.Encode()
}

func cut(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Test seams for the token sources.
var (
	keychainToken = func(ctx context.Context, repo string) (string, error) {
		out, err := exec.CommandContext(ctx, "security", "find-generic-password", "-s", keychainService, "-a", repo, "-w").Output()
		return strings.TrimSpace(string(out)), err
	}
	ghCLIToken = func(ctx context.Context) (string, error) {
		out, err := exec.CommandContext(ctx, "gh", "auth", "token").Output()
		return strings.TrimSpace(string(out)), err
	}
)

// githubToken prefers GH_TOKEN, then the repo's Keychain token from localci
// auth, then the gh CLI's token, which covers every repo. The token is held in
// memory only.
func githubToken(ctx context.Context, repo string, logger *slog.Logger) (string, error) {
	if t := os.Getenv("GH_TOKEN"); t != "" {
		return t, nil
	}
	t, err := keychainToken(ctx, repo)
	if err == nil && t != "" {
		return t, nil
	}
	logger.Debug("no Keychain token", "repo", repo, "err", err)
	t, err = ghCLIToken(ctx)
	if err == nil && t == "" {
		err = errors.New("empty token")
	}
	if err != nil {
		return "", fmt.Errorf("no token: run `localci auth -repo %s`, or `gh auth login`: %v", repo, err)
	}
	logger.Info("using the gh CLI token, which covers every repo; run `localci auth -repo " + repo + "` for a token scoped to this one")
	return t, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
