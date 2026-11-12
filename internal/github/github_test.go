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
				_, _ = w.Write([]byte(tt.resp))
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

func TestSetRoute(t *testing.T) {
	tests := []struct {
		name       string
		current    string // "" means the variable doesn't exist
		label      string
		wantMethod string // "" means no write
		wantValue  string
		wantErr    string
	}{
		{name: "create variable", label: "localci-me", wantMethod: "POST", wantValue: `{"me":"localci-me"}`},
		{name: "add keeps others", current: `{"you":"localci-you"}`, label: "localci-me", wantMethod: "PATCH", wantValue: `{"me":"localci-me","you":"localci-you"}`},
		{name: "remove keeps others", current: `{"me":"localci-me","you":"localci-you"}`, wantMethod: "PATCH", wantValue: `{"you":"localci-you"}`},
		{name: "already set", current: `{"me":"localci-me"}`, label: "localci-me"},
		{name: "remove when absent", current: `{}`},
		{name: "remove when no variable"},
		{name: "not JSON", current: `nope`, label: "localci-me", wantErr: "not a JSON object"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotMethod, gotValue string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/actions/variables/LOCALCI_RUNNERS":
					if tt.current == "" {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"name": "LOCALCI_RUNNERS", "value": tt.current})
				case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/actions/variables",
					r.Method == http.MethodPatch && r.URL.Path == "/repos/o/r/actions/variables/LOCALCI_RUNNERS":
					var body struct{ Name, Value string }
					_ = json.NewDecoder(r.Body).Decode(&body)
					gotMethod, gotValue = r.Method, body.Value
					if r.Method == http.MethodPost {
						if body.Name != "LOCALCI_RUNNERS" {
							t.Errorf("created variable %q", body.Name)
						}
						w.WriteHeader(http.StatusCreated)
					} else {
						w.WriteHeader(http.StatusNoContent)
					}
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
			}))
			defer srv.Close()

			c := New("tok")
			c.BaseURL = srv.URL
			err := c.SetRoute(context.Background(), "o", "r", "me", tt.label)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if gotMethod != tt.wantMethod || gotValue != tt.wantValue {
				t.Errorf("write = %s %s, want %s %s", gotMethod, gotValue, tt.wantMethod, tt.wantValue)
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

func TestWorkflows(t *testing.T) {
	tests := []struct {
		name  string
		noDir bool
		want  map[string]string
	}{
		{name: "yaml files only", want: map[string]string{"ci.yml": "on: push\n", "deploy.yaml": "on: release\n"}},
		{name: "no workflows dir", noDir: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				const dir = "/repos/o/r/contents/.github/workflows"
				switch r.URL.Path {
				case dir:
					if tt.noDir {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					_, _ = w.Write([]byte(`[{"name":"ci.yml","type":"file"},{"name":"deploy.yaml","type":"file"},{"name":"README.md","type":"file"},{"name":"sub","type":"dir"}]`))
				case dir + "/ci.yml":
					// The API wraps base64 content every 60 characters.
					_, _ = w.Write([]byte(`{"content":"b246IHB1c2gK\n"}`))
				case dir + "/deploy.yaml":
					_, _ = w.Write([]byte(`{"content":"b246IHJlbGVhc2UK"}`))
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()

			c := New("tok")
			c.BaseURL = srv.URL
			got, err := c.Workflows(context.Background(), "o", "r")
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d files, want %d", len(got), len(tt.want))
			}
			for name, src := range tt.want {
				if string(got[name]) != src {
					t.Errorf("%s = %q, want %q", name, got[name], src)
				}
			}
		})
	}
}

func TestDefaultWorkflowPermissions(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		resp    string
		want    string
		wantErr bool
	}{
		{name: "read", status: 200, resp: `{"default_workflow_permissions":"read","can_approve_pull_request_reviews":false}`, want: "read"},
		{name: "write", status: 200, resp: `{"default_workflow_permissions":"write","can_approve_pull_request_reviews":true}`, want: "write"},
		{name: "not admin", status: 403, resp: `{"message":"Must have admin rights"}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/repos/o/r/actions/permissions/workflow" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.resp))
			}))
			defer srv.Close()

			c := New("tok")
			c.BaseURL = srv.URL
			got, err := c.DefaultWorkflowPermissions(context.Background(), "o", "r")
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
