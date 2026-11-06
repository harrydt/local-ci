// Package runner keeps one ephemeral GitHub Actions runner alive inside a
// Lima VM: register via JIT config, run exactly one job, clean up, repeat.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/teeplabs/localci/internal/github"
)

// runScript reads the JIT config from stdin so it never appears in argv on the host.
const runScript = `read -r c; cd ~/actions-runner && exec ./run.sh --jitconfig "$c"`

// pruneScript frees disk when the VM's root filesystem passes 80%. Images,
// volumes and build cache persist between jobs (warm pulls) until then.
const pruneScript = `used=$(df --output=pcent / | tail -1 | tr -dc 0-9)
if [ "$used" -ge 80 ]; then echo "disk ${used}% used; pruning Docker"; docker system prune -af --volumes; fi`

const cleanupTimeout = 15 * time.Second

type Agent struct {
	GitHub      *github.Client
	Owner, Repo string
	Label       string // runner label; must match the routing in LOCALCI_RUNNERS
	VM          string // Lima instance name
	Log         *slog.Logger

	// limactl runs `limactl args...`; nil means the real binary. Test seam.
	limactl    func(ctx context.Context, stdin io.Reader, args ...string) error
	retryDelay time.Duration
}

// Run loops until ctx is cancelled. Failures of a single runner session are
// logged and retried; only setup failures are returned.
func (a *Agent) Run(ctx context.Context) error {
	if a.limactl == nil {
		a.limactl = execLimactl
	}
	if a.retryDelay == 0 {
		a.retryDelay = 10 * time.Second
	}

	if err := a.limactl(ctx, nil, "start", "--tty=false", a.VM); err != nil {
		return fmt.Errorf("start VM %q: %w", a.VM, err)
	}
	if err := a.limactl(ctx, nil, "shell", a.VM, "--", "test", "-x", "actions-runner/run.sh"); err != nil {
		return fmt.Errorf("runner not installed in VM %q (run scripts/setup-vm.sh): %w", a.VM, err)
	}

	for ctx.Err() == nil {
		if err := a.session(ctx); err != nil {
			a.Log.Error("runner session failed; retrying", "err", err, "retry_in", a.retryDelay)
			select {
			case <-ctx.Done():
			case <-time.After(a.retryDelay):
			}
		}
	}
	return nil
}

// session registers one ephemeral runner and blocks until it exits.
func (a *Agent) session(ctx context.Context) error {
	name := fmt.Sprintf("%s-%d", a.Label, time.Now().UnixNano())
	start := time.Now()
	cfg, err := a.GitHub.GenerateJITConfig(ctx, a.Owner, a.Repo, name, []string{a.Label})
	if err != nil {
		return fmt.Errorf("generate JIT config: %w", err)
	}
	log := a.Log.With("runner", name, "runner_id", cfg.RunnerID)
	log.Info("runner registered; waiting for job", "register_ms", time.Since(start).Milliseconds())
	defer a.cleanup(log, cfg.RunnerID)

	start = time.Now()
	err = a.limactl(ctx, strings.NewReader(cfg.Encoded+"\n"), "shell", a.VM, "--", "bash", "-c", runScript)
	log.Info("runner exited", "session_s", int(time.Since(start).Seconds()), "err", err)
	if ctx.Err() != nil {
		return nil
	}
	if perr := a.limactl(ctx, nil, "shell", a.VM, "--", "bash", "-c", pruneScript); perr != nil {
		log.Warn("prune Docker", "err", perr)
	}
	return err
}

// cleanup makes sure no listener outlives the session and the registration is
// gone. Killing limactl does not reliably stop the process inside the VM, and
// a runner killed before taking a job stays registered on GitHub.
func (a *Agent) cleanup(log *slog.Logger, runnerID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if err := a.limactl(ctx, nil, "shell", a.VM, "--", "bash", "-c", "pkill -f [R]unner.Listener || true"); err != nil {
		log.Warn("kill runner in VM", "err", err)
	}
	if err := a.GitHub.DeleteRunner(ctx, a.Owner, a.Repo, runnerID); err != nil {
		log.Warn("delete runner registration", "err", err)
	}
}

func execLimactl(ctx context.Context, stdin io.Reader, args ...string) error {
	cmd := exec.CommandContext(ctx, "limactl", args...)
	cmd.Stdin = stdin
	// ponytail: runner output passes straight through; pipe into slog if it needs structure.
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Errorf("limactl %s: exit %d", args[0], exitErr.ExitCode())
	}
	return err
}
