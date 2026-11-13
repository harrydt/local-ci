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
	"sync"
	"time"

	"github.com/teeplabs/localci/internal/github"
)

// runScript reads the JIT config from stdin so it never appears in argv on the host.
// Each slot gets its own HOME, like separate hosted machines: tools such as
// rustup corrupt a shared ~/.rustup when two jobs install at once. With the
// local cache on (localci setup), it also points the job's cache and results
// client at the server in the VM; the patched runner leaves that set. The
// ~/.localci paths expand before HOME changes, so they stay the VM user's.
func runScript(dir string) string {
	return fmt.Sprintf(`read -r c; cd ~/%s && mkdir -p _home && if [ -f ~/.localci/cache-url ]; then export ACTIONS_RESULTS_URL=$(cat ~/.localci/cache-url) ACTIONS_RUNNER_HOOK_JOB_STARTED=~/.localci/job-started.sh; fi; HOME=$PWD/_home exec ./run.sh --jitconfig "$c"`, dir)
}

// runnerDir is slot i's runner directory in the VM. A runner writes its
// credentials and _work into its directory, so parallel runners need one each.
// Slot 1 keeps the original directory and its warm tool cache.
func runnerDir(i int) string {
	if i == 1 {
		return "actions-runner"
	}
	return fmt.Sprintf("actions-runner-%d", i)
}

// copyScript creates or refreshes the extra runner directories from the
// installed one, without its credentials, logs, work tree or HOME. It syncs
// every time (unchanged files are skipped), so the copies follow a runner
// version bump or the cache patch; excluded paths are never deleted.
const copyScript = `for i in $(seq 2 %d); do
  rsync -a --delete --exclude=_work --exclude=_diag --exclude=_home --exclude='.credentials*' --exclude=.runner ~/actions-runner/ ~/actions-runner-$i/
done`

// ponytail: with Runners > 1 a prune can delete an image another job pulled but
// hasn't started yet; only near a full disk. Serialize prune with jobs if it bites.
// pruneScript frees disk when the VM's root filesystem passes 80%. Images,
// volumes and build cache persist between jobs (warm pulls) until then.
const pruneScript = `used=$(df --output=pcent / | tail -1 | tr -dc 0-9)
if [ "$used" -ge 80 ]; then echo "disk ${used}% used; pruning Docker"; docker system prune -af --volumes; fi`

// cacheCheckScript passes when the local cache is off, or on and healthy.
const cacheCheckScript = `[ ! -f ~/.localci/cache-url ] || curl -fsS -m 5 "$(cat ~/.localci/cache-url)healthz" >/dev/null`

const cleanupTimeout = 15 * time.Second

type Agent struct {
	GitHub      *github.Client
	Owner, Repo string
	Login       string // GitHub user whose jobs are routed here
	Label       string // runner label; Run maps Login to it in LOCALCI_RUNNERS
	Runners     int    // runners in parallel, one job each; 0 means 1
	VM          string // Lima instance name
	Log         *slog.Logger

	// limactl runs `limactl args...`; nil means the real binary. Test seam.
	limactl    func(ctx context.Context, stdin io.Reader, args ...string) error
	retryDelay time.Duration
}

// Run loops until ctx is cancelled. Failures of a single runner session are
// logged and retried; only setup failures are returned. While it runs, Login's
// jobs are routed to Label; once ctx is cancelled they go back to hosted runners.
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
		return fmt.Errorf("runner not installed in VM %q (run localci setup): %w", a.VM, err)
	}
	// With the local cache on, the patched runner sends artifact uploads
	// through it too, so jobs would fail if it were down.
	if err := a.limactl(ctx, nil, "shell", a.VM, "--", "bash", "-c", cacheCheckScript); err != nil {
		return fmt.Errorf("local cache is on but not answering in VM %q (run localci setup, or localci setup -cache=false): %w", a.VM, err)
	}
	n := max(a.Runners, 1)
	if n > 1 {
		if err := a.limactl(ctx, nil, "shell", a.VM, "--", "bash", "-c", fmt.Sprintf(copyScript, n)); err != nil {
			return fmt.Errorf("create runner directories in VM %q: %w", a.VM, err)
		}
	}

	if err := a.GitHub.SetRoute(ctx, a.Owner, a.Repo, a.Login, a.Label); err != nil {
		return fmt.Errorf("route %s's jobs to %s: %w", a.Login, a.Label, err)
	}
	a.Log.Info("routing jobs here", "login", a.Login)

	var wg sync.WaitGroup
	for i := 1; i <= n; i++ {
		wg.Go(func() { a.loop(ctx, i) })
	}
	<-ctx.Done()
	// Unroute before the runners go away, so new jobs don't queue for them.
	// ponytail: SIGKILL or a crash skips this and leaves jobs queued until the
	// next localci up; a scheduled sweep would need a backend (ADR 0002).
	uctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if err := a.GitHub.SetRoute(uctx, a.Owner, a.Repo, a.Login, ""); err != nil {
		a.Log.Error("unroute failed; remove your entry from LOCALCI_RUNNERS by hand", "login", a.Login, "err", err)
	} else {
		a.Log.Info("jobs routed back to hosted runners", "login", a.Login)
	}
	wg.Wait()
	return nil
}

// loop keeps one runner slot busy until ctx is cancelled.
func (a *Agent) loop(ctx context.Context, slot int) {
	log := a.Log.With("slot", slot)
	for ctx.Err() == nil {
		if err := a.session(ctx, log, slot); err != nil {
			log.Error("runner session failed; retrying", "err", err, "retry_in", a.retryDelay)
			select {
			case <-ctx.Done():
			case <-time.After(a.retryDelay):
			}
		}
	}
}

// session registers one ephemeral runner and blocks until it exits.
func (a *Agent) session(ctx context.Context, log *slog.Logger, slot int) error {
	dir := runnerDir(slot)
	name := fmt.Sprintf("%s-%d-%d", a.Label, slot, time.Now().UnixNano())
	start := time.Now()
	cfg, err := a.GitHub.GenerateJITConfig(ctx, a.Owner, a.Repo, name, []string{a.Label})
	if err != nil {
		return fmt.Errorf("generate JIT config: %w", err)
	}
	log = log.With("runner", name, "runner_id", cfg.RunnerID)
	log.Info("runner registered; waiting for job", "register_ms", time.Since(start).Milliseconds())
	defer a.cleanup(log, dir, cfg.RunnerID)

	start = time.Now()
	err = a.limactl(ctx, strings.NewReader(cfg.Encoded+"\n"), "shell", a.VM, "--", "bash", "-c", runScript(dir))
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
func (a *Agent) cleanup(log *slog.Logger, dir string, runnerID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if err := a.limactl(ctx, nil, "shell", a.VM, "--", "bash", "-c", killScript(dir)); err != nil {
		log.Warn("kill runner in VM", "err", err)
	}
	if err := a.GitHub.DeleteRunner(ctx, a.Owner, a.Repo, runnerID); err != nil {
		log.Warn("delete runner registration", "err", err)
	}
}

// killScript stops only dir's listener. The bracket keeps pkill from matching
// its own bash -c command line.
func killScript(dir string) string {
	return fmt.Sprintf("pkill -f '[%c]%s/bin/Runner.Listener' || true", dir[0], dir[1:])
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
