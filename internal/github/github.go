// Package github is the minimal GitHub REST client LocalCI needs: registering
// ephemeral JIT runners and deleting them.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
