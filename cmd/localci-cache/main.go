// Command localci-cache serves GitHub's Actions cache API from the VM's disk.
// localci setup installs it in the VM as a systemd service and points the
// runner at it; see internal/cache.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/harrydt/local-ci/internal/cache"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "localci-cache:", err)
		os.Exit(1)
	}
}

func run() error {
	home, _ := os.UserHomeDir()
	addr := flag.String("addr", ":7380", "listen address")
	dir := flag.String("dir", filepath.Join(home, ".cache", "localci-cache"), "storage directory")
	maxMB := flag.Int64("max-size-mb", 5120, "evict least recently used entries above this size")
	upstream := flag.String("upstream", cache.DefaultUpstream, "GitHub results service, for non-cache requests")
	flag.Parse()

	up, err := url.Parse(*upstream)
	if err != nil || up.Scheme == "" {
		return fmt.Errorf("invalid -upstream %q", *upstream)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	s := &cache.Server{Dir: *dir, MaxBytes: *maxMB << 20, Upstream: up, Log: log}
	srv := &http.Server{Addr: *addr, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	log.Info("serving", "addr", *addr, "dir", *dir, "max_mb", *maxMB, "upstream", up.String())
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
