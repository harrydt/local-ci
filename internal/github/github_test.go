package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGenerateJITConfig(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		resp    string
		wantErr string
		wantID  int64
	}{
		{name: "created", status: 201, resp: `{"runner":{"id":42},"encoded_jit_config":"secret"}`, wantID: 42},
		{name: "repo not found", status: 404, resp: `{"message":"Not Found"}`, wantErr: "not found"},
		{name: "name conflict", status: 409, resp: `{"message":"Already exists"}`, wantErr: "HTTP 409: {\"message\":\"Already exists\"}"},
		{name: "validation", status: 422, resp: `{"message":"Invalid"}`, wantErr: "HTTP 422"},
		{name: "empty config", status: 201, resp: `{"runner":{"id":1}}`, wantErr: "empty encoded_jit_config"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/repos/o/r/actions/runners/generate-jitconfig" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer tok" {
					t.Errorf("Authorization = %q", got)
				}
				var body struct {
					Name    string   `json:"name"`
					GroupID int      `json:"runner_group_id"`
					Labels  []string `json:"labels"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode body: %v", err)
				}
				if body.Name != "n" || body.GroupID != 1 || len(body.Labels) != 1 || body.Labels[0] != "l" {
					t.Errorf("body = %+v", body)
				}
				w.WriteHeader(tt.status)
				w.Write([]byte(tt.resp))
			}))
			defer srv.Close()

			c := New("tok")
			c.BaseURL = srv.URL
			cfg, err := c.GenerateJITConfig(context.Background(), "o", "r", "n", []string{"l"})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.RunnerID != tt.wantID || cfg.Encoded != "secret" {
				t.Errorf("cfg = %+v", cfg)
			}
		})
	}
}

func TestDeleteRunner(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		wantErr bool
	}{
		{name: "deleted", status: 204},
		{name: "already gone", status: 404},
		{name: "busy", status: 422, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete || r.URL.Path != "/repos/o/r/actions/runners/7" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(tt.status)
			}))
			defer srv.Close()

			c := New("tok")
			c.BaseURL = srv.URL
			err := c.DeleteRunner(context.Background(), "o", "r", 7)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCancelledContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("request should not be sent")
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := New("tok")
	c.BaseURL = srv.URL
	_, err := c.GenerateJITConfig(ctx, "o", "r", "n", []string{"l"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
