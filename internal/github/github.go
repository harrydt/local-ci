// Package github is the minimal GitHub REST client LocalCI needs: registering
// ephemeral JIT runners, deleting them, and routing jobs via LOCALCI_RUNNERS.
package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrNotFound is returned for HTTP 404 responses.
var ErrNotFound = errors.New("not found")

// defaultRunnerGroupID is the "Default" group, which repo-level runners use.
// The runner-groups lookup endpoint needs admin:org, so it is hardcoded.
const defaultRunnerGroupID = 1

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func New(token string) *Client {
	return &Client{
		BaseURL: "https://api.github.com",
		Token:   token,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

// JITConfig is a single-use runner registration. Encoded is a credential:
// pass it straight to the runner and never log it.
type JITConfig struct {
	RunnerID int64
	Encoded  string
}

// GenerateJITConfig registers an ephemeral runner on owner/repo with the given labels.
func (c *Client) GenerateJITConfig(ctx context.Context, owner, repo, name string, labels []string) (JITConfig, error) {
	body := map[string]any{"name": name, "runner_group_id": defaultRunnerGroupID, "labels": labels}
	var resp struct {
		Runner struct {
			ID int64 `json:"id"`
		} `json:"runner"`
		EncodedJITConfig string `json:"encoded_jit_config"`
	}
	path := fmt.Sprintf("/repos/%s/%s/actions/runners/generate-jitconfig", url.PathEscape(owner), url.PathEscape(repo))
	if err := c.do(ctx, http.MethodPost, path, body, &resp, http.StatusCreated); err != nil {
		return JITConfig{}, err
	}
	if resp.EncodedJITConfig == "" {
		return JITConfig{}, errors.New("generate-jitconfig: empty encoded_jit_config")
	}
	return JITConfig{RunnerID: resp.Runner.ID, Encoded: resp.EncodedJITConfig}, nil
}

// DeleteRunner removes a runner registration. A runner that is already gone
// (ephemeral runners deregister after their job) is not an error.
func (c *Client) DeleteRunner(ctx context.Context, owner, repo string, id int64) error {
	path := fmt.Sprintf("/repos/%s/%s/actions/runners/%d", url.PathEscape(owner), url.PathEscape(repo), id)
	err := c.do(ctx, http.MethodDelete, path, nil, nil, http.StatusNoContent)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// Login returns the login of the token's user.
func (c *Client) Login(ctx context.Context) (string, error) {
	var resp struct {
		Login string `json:"login"`
	}
	if err := c.do(ctx, http.MethodGet, "/user", nil, &resp, http.StatusOK); err != nil {
		return "", err
	}
	return resp.Login, nil
}

// RoutingVar is the repo variable workflows read in runs-on: a JSON object
// mapping a GitHub login to that user's runner label.
const RoutingVar = "LOCALCI_RUNNERS"

// SetRoute sets login's entry in RoutingVar to label, or removes it when label
// is empty. Other users' entries are kept. Needs only write access.
//
// ponytail: read-modify-write with no conditional update, so two users starting
// or stopping in the same instant can lose one edit. Per-user variables if it bites.
func (c *Client) SetRoute(ctx context.Context, owner, repo, login, label string) error {
	base := fmt.Sprintf("/repos/%s/%s/actions/variables", url.PathEscape(owner), url.PathEscape(repo))
	var v struct {
		Value string `json:"value"`
	}
	err := c.do(ctx, http.MethodGet, base+"/"+RoutingVar, nil, &v, http.StatusOK)
	exists := err == nil
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}

	routes := map[string]string{}
	if exists && v.Value != "" {
		if err := json.Unmarshal([]byte(v.Value), &routes); err != nil {
			return fmt.Errorf("%s is not a JSON object of login to label: %w", RoutingVar, err)
		}
	}
	if routes[login] == label {
		return nil
	}
	if label == "" {
		delete(routes, login)
	} else {
		routes[login] = label
	}
	b, _ := json.Marshal(routes) // a map[string]string always marshals

	if !exists {
		return c.do(ctx, http.MethodPost, base, map[string]string{"name": RoutingVar, "value": string(b)}, nil, http.StatusCreated)
	}
	return c.do(ctx, http.MethodPatch, base+"/"+RoutingVar, map[string]string{"value": string(b)}, nil, http.StatusNoContent)
}

// DefaultWorkflowPermissions returns the repo's default GITHUB_TOKEN
// permissions: "read" or "write".
func (c *Client) DefaultWorkflowPermissions(ctx context.Context, owner, repo string) (string, error) {
	var resp struct {
		Default string `json:"default_workflow_permissions"`
	}
	path := fmt.Sprintf("/repos/%s/%s/actions/permissions/workflow", url.PathEscape(owner), url.PathEscape(repo))
	if err := c.do(ctx, http.MethodGet, path, nil, &resp, http.StatusOK); err != nil {
		return "", err
	}
	return resp.Default, nil
}

// Workflows returns the workflow files on the default branch, by file name.
// A repo without .github/workflows has none.
func (c *Client) Workflows(ctx context.Context, owner, repo string) (map[string][]byte, error) {
	base := fmt.Sprintf("/repos/%s/%s/contents/.github/workflows", url.PathEscape(owner), url.PathEscape(repo))
	var entries []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	err := c.do(ctx, http.MethodGet, base, nil, &entries, http.StatusOK)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for _, e := range entries {
		if e.Type != "file" || !strings.HasSuffix(e.Name, ".yml") && !strings.HasSuffix(e.Name, ".yaml") {
			continue
		}
		var f struct {
			Content string `json:"content"`
		}
		if err := c.do(ctx, http.MethodGet, base+"/"+url.PathEscape(e.Name), nil, &f, http.StatusOK); err != nil {
			return nil, err
		}
		b, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(f.Content, "\n", ""))
		if err != nil {
			return nil, fmt.Errorf("decode %s: %w", e.Name, err)
		}
		files[e.Name] = b
	}
	return files, nil
}

func (c *Client) do(ctx context.Context, method, path string, in, out any, want int) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != want {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		if resp.StatusCode == http.StatusNotFound {
			return fmt.Errorf("%s %s: %w", method, path, ErrNotFound)
		}
		return fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, bytes.TrimSpace(msg))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("%s %s: decode response: %w", method, path, err)
		}
	}
	return nil
}
