package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teeplabs/localci/internal/github"
)

// fakeGitHub counts JIT registrations and deletions, and records the
// LOCALCI_RUNNERS values written.
type fakeGitHub struct {
	mu                 sync.Mutex
	registered, delete int
	failRegister       bool
	routes             []string
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case strings.Contains(r.URL.Path, "/actions/variables"):
		switch r.Method {
		case http.MethodGet:
			if len(f.routes) == 0 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"value": f.routes[len(f.routes)-1]})
		default:
			var body struct{ Value string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.routes = append(f.routes, body.Value)
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusCreated)
			} else {
				w.WriteHeader(http.StatusNoContent)
			}
		}
	case r.Method == http.MethodPost && f.failRegister:
		w.WriteHeader(http.StatusInternalServerError)
	case r.Method == http.MethodPost:
		f.registered++
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"runner":{"id":9},"encoded_jit_config":"jit-secret"}`))
	case r.Method == http.MethodDelete:
		f.delete++
		w.WriteHeader(http.StatusNoContent)
	}
}

func newAgent(t *testing.T, gh *fakeGitHub, limactl func(context.Context, io.Reader, ...string) error) *Agent {
	srv := httptest.NewServer(gh)
	t.Cleanup(srv.Close)
	c := github.New("tok")
	c.BaseURL = srv.URL
	return &Agent{
		GitHub: c, Owner: "o", Repo: "r", Login: "me", Label: "localci-test", VM: "vm",
		Log:        slog.New(slog.DiscardHandler),
		limactl:    limactl,
		retryDelay: time.Millisecond,
	}
}

func TestRunLoopsAndCleansUp(t *testing.T) {
	gh := &fakeGitHub{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	jobs, prunes := 0, 0
	agent := newAgent(t, gh, func(ctx context.Context, stdin io.Reader, args ...string) error {
		if len(args) > 5 && args[5] == pruneScript {
			mu.Lock()
			prunes++
			mu.Unlock()
		}
		if len(args) > 5 && args[5] == runScript("actions-runner") {
			b, _ := io.ReadAll(stdin)
			if string(b) != "jit-secret\n" {
				t.Errorf("runner stdin = %q", b)
			}
			mu.Lock()
			defer mu.Unlock()
			if jobs++; jobs == 3 {
				cancel()
			}
		}
		return nil
	})

	if err := agent.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if gh.registered != 3 || gh.delete != 3 {
		t.Errorf("registered=%d deleted=%d, want 3 and 3", gh.registered, gh.delete)
	}
	if want := []string{`{"me":"localci-test"}`, `{}`}; !slices.Equal(gh.routes, want) {
		t.Errorf("routes written = %q, want %q", gh.routes, want)
	}
	if prunes != 2 {
		t.Errorf("prunes=%d, want 2 (none after the session cancelled by Ctrl-C)", prunes)
	}
}

func TestRunParallel(t *testing.T) {
	gh := &fakeGitHub{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	started := make(chan string)
	var mu sync.Mutex
	var copied, killed []string
	agent := newAgent(t, gh, func(ctx context.Context, _ io.Reader, args ...string) error {
		if len(args) < 6 {
			return nil
		}
		mu.Lock()
		defer mu.Unlock()
		switch script := args[5]; {
		case strings.Contains(script, "rsync"):
			copied = append(copied, script)
		case strings.HasPrefix(script, "pkill"):
			killed = append(killed, script)
		case strings.Contains(script, "exec ./run.sh"):
			mu.Unlock()
			started <- script
			<-ctx.Done() // the job runs until Ctrl-C
			mu.Lock()
		}
		return nil
	})
	agent.Runners = 2

	done := make(chan error)
	go func() { done <- agent.Run(ctx) }()
	got := map[string]bool{}
	for range 2 { // both runners must be busy at once, or this times out
		select {
		case s := <-started:
			got[s] = true
		case <-time.After(5 * time.Second):
			t.Fatal("second runner never started while the first was busy")
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	if !got[runScript("actions-runner")] || !got[runScript("actions-runner-2")] {
		t.Errorf("runner scripts = %v, want one per directory", got)
	}
	for s := range got {
		if !strings.Contains(s, "HOME=$PWD/_home") {
			t.Errorf("runner script %q shares HOME with other slots", s)
		}
	}
	if len(copied) != 1 || !strings.Contains(copied[0], "seq 2 2") {
		t.Errorf("copy scripts = %q, want one creating actions-runner-2", copied)
	}
	if gh.registered != 2 || gh.delete != 2 {
		t.Errorf("registered=%d deleted=%d, want 2 and 2", gh.registered, gh.delete)
	}
	if len(killed) != 2 || killed[0] == killed[1] {
		t.Errorf("kill scripts = %q, want one per runner directory", killed)
	}
}

func TestRunRetriesRegistrationFailure(t *testing.T) {
	gh := &fakeGitHub{failRegister: true}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	agent := newAgent(t, gh, func(context.Context, io.Reader, ...string) error { return nil })

	if err := agent.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if gh.delete != 0 {
		t.Errorf("deleted=%d, want 0 when nothing was registered", gh.delete)
	}
}

func TestRunSetupErrors(t *testing.T) {
	tests := []struct {
		name    string
		failOn  string
		wantErr string
	}{
		{name: "VM start fails", failOn: "start", wantErr: `start VM "vm"`},
		{name: "runner missing", failOn: "test", wantErr: "runner not installed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := newAgent(t, &fakeGitHub{}, func(_ context.Context, _ io.Reader, args ...string) error {
				if strings.Contains(strings.Join(args, " "), tt.failOn) {
					return errors.New("boom")
				}
				return nil
			})
			err := agent.Run(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}
