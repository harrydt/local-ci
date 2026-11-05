package runner

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teeplabs/localci/internal/github"
)

// fakeGitHub counts JIT registrations and deletions.
type fakeGitHub struct {
	mu                 sync.Mutex
	registered, delete int
	failRegister       bool
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && f.failRegister:
		w.WriteHeader(http.StatusInternalServerError)
	case r.Method == http.MethodPost:
		f.registered++
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"runner":{"id":9},"encoded_jit_config":"jit-secret"}`))
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
		GitHub: c, Owner: "o", Repo: "r", Label: "localci-test", VM: "vm",
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
	jobs := 0
	agent := newAgent(t, gh, func(ctx context.Context, stdin io.Reader, args ...string) error {
		if len(args) > 4 && args[4] == "-c" && args[5] == runScript {
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
