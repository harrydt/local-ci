package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantErr    string
		wantStdout string
	}{
		{name: "version", args: []string{"version"}, wantStdout: "dev\n"},
		{name: "version with log level", args: []string{"-log-level", "debug", "version"}, wantStdout: "dev\n"},
		{name: "missing command", args: nil, wantErr: "missing command"},
		{name: "unknown command", args: []string{"nope"}, wantErr: `unknown command "nope"`},
		{name: "bad log level", args: []string{"-log-level", "loud", "version"}, wantErr: "invalid -log-level"},
		{name: "up without repo", args: []string{"up", "-label", "l"}, wantErr: "-repo must be owner/name"},
		{name: "up bad repo", args: []string{"up", "-repo", "a/b/c", "-label", "l"}, wantErr: "-repo must be owner/name"},
		{name: "auth without repo", args: []string{"auth"}, wantErr: "-repo must be owner/name"},
		{name: "up zero runners", args: []string{"up", "-repo", "o/r", "-runners", "0"}, wantErr: "-runners must be at least 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := run(tt.args, &stdout, &stderr)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
			if got := stdout.String(); got != tt.wantStdout {
				t.Errorf("stdout = %q, want %q", got, tt.wantStdout)
			}
		})
	}
}

func TestGithubToken(t *testing.T) {
	origKC, origGH := keychainToken, ghCLIToken
	t.Cleanup(func() { keychainToken, ghCLIToken = origKC, origGH })
	missing := func() (string, error) { return "", errors.New("missing") }
	tests := []struct {
		name               string
		env                string
		keychain, ghCLI    func() (string, error)
		want, wantErr, log string
	}{
		{name: "GH_TOKEN wins", env: "tok-env", keychain: func() (string, error) { return "tok-kc", nil }, want: "tok-env"},
		{name: "keychain before gh", keychain: func() (string, error) { return "tok-kc", nil }, ghCLI: func() (string, error) { return "tok-gh", nil }, want: "tok-kc"},
		{name: "gh fallback with notice", keychain: missing, ghCLI: func() (string, error) { return "tok-gh", nil }, want: "tok-gh", log: "covers every repo"},
		{name: "empty keychain item falls back", keychain: func() (string, error) { return "", nil }, ghCLI: func() (string, error) { return "tok-gh", nil }, want: "tok-gh", log: "covers every repo"},
		{name: "nothing", keychain: missing, ghCLI: missing, wantErr: "localci auth -repo o/r"},
		{name: "empty gh token", keychain: missing, ghCLI: func() (string, error) { return "", nil }, wantErr: "empty token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GH_TOKEN", tt.env)
			keychainToken = func(context.Context, string) (string, error) { return tt.keychain() }
			ghCLIToken = func(context.Context) (string, error) { return tt.ghCLI() }
			var logs bytes.Buffer
			got, err := githubToken(context.Background(), "o/r", slog.New(slog.NewTextHandler(&logs, nil)))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
			if tt.log == "" && logs.Len() > 0 || !strings.Contains(logs.String(), tt.log) {
				t.Errorf("logs = %q, want %q", logs.String(), tt.log)
			}
			if strings.Contains(logs.String(), got) {
				t.Errorf("logs contain the token: %q", logs.String())
			}
		})
	}
}

func TestTokenURL(t *testing.T) {
	u, err := url.Parse(tokenURL("octo-org/hello"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	for k, v := range map[string]string{"target_name": "octo-org", "administration": "write", "actions_variables": "write", "contents": "read", "name": "localci octo-org/hello"} {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, q.Get(k), v)
		}
	}
}

func TestLockVM(t *testing.T) {
	dir := t.TempDir()
	release, err := lockVM(dir, "localci")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockVM(dir, "localci"); err == nil || !strings.Contains(err.Error(), "another localci up") {
		t.Fatalf("second lock err = %v, want another localci up", err)
	}
	other, err := lockVM(dir, "other")
	if err != nil {
		t.Fatalf("lock for a different VM: %v", err)
	}
	other()
	release()
	again, err := lockVM(dir, "localci")
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	again()
}
