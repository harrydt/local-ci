package main

import (
	"bytes"
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
		{name: "up without label", args: []string{"up", "-repo", "o/r"}, wantErr: "-label is required"},
		{name: "up zero runners", args: []string{"up", "-repo", "o/r", "-label", "l", "-runners", "0"}, wantErr: "-runners must be at least 1"},
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
