package cache

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// token builds an unsigned job token with the given repository and cache
// scopes (name:permission).
func token(repo string, scopes ...string) string {
	var ac []map[string]any
	for _, s := range scopes {
		name, perm, _ := strings.Cut(s, ":")
		var p int
		_, _ = fmt.Sscan(perm, &p)
		ac = append(ac, map[string]any{"Scope": name, "Permission": p})
	}
	acJSON, _ := json.Marshal(ac)
	payload, _ := json.Marshal(map[string]string{"ac": string(acJSON), "repository_id": repo})
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc(payload) + ".sig"
}

// Tokens as GitHub issues them: a job on main reads and writes main; a job on
// a feature branch writes its branch and also reads main.
var (
	mainTok    = token("1", "refs/heads/main:3")
	featureTok = token("1", "refs/heads/feat:3", "refs/heads/main:1")
	otherRepo  = token("2", "refs/heads/main:3")
)

func newServer(t *testing.T, upstream string, maxBytes int64) *httptest.Server {
	t.Helper()
	u, _ := url.Parse(upstream)
	s := &Server{Dir: t.TempDir(), MaxBytes: maxBytes, Upstream: u}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func rpc(t *testing.T, srv *httptest.Server, method, tok string, body any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+twirpPrefix+method, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{"status": resp.StatusCode}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out
}

func put(t *testing.T, rawURL, query, body string) int {
	t.Helper()
	if query != "" {
		rawURL += "&" + query
	}
	req, _ := http.NewRequest(http.MethodPut, rawURL, strings.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// save stores content under key the way @actions/cache does: reserve, upload
// (in blocks when there's more than one part), commit, finalize.
func save(t *testing.T, srv *httptest.Server, tok, key, version string, parts ...string) {
	t.Helper()
	res := rpc(t, srv, "CreateCacheEntry", tok, map[string]string{"key": key, "version": version})
	if res["ok"] != true {
		t.Fatalf("create %q: %v", key, res)
	}
	up := res["signed_upload_url"].(string)
	size := 0
	if len(parts) == 1 {
		if c := put(t, up, "", parts[0]); c != http.StatusCreated {
			t.Fatalf("put blob: %d", c)
		}
		size = len(parts[0])
	} else {
		var list strings.Builder
		list.WriteString(`<?xml version="1.0" encoding="utf-8"?><BlockList>`)
		// Upload in reverse to check that the list, not arrival, sets the order.
		for i := len(parts) - 1; i >= 0; i-- {
			id := base64.StdEncoding.EncodeToString(fmt.Appendf(nil, "block-%04d", i))
			if c := put(t, up, "comp=block&blockid="+url.QueryEscape(id), parts[i]); c != http.StatusCreated {
				t.Fatalf("put block: %d", c)
			}
			size += len(parts[i])
		}
		for i := range parts {
			fmt.Fprintf(&list, "<Latest>%s</Latest>", base64.StdEncoding.EncodeToString(fmt.Appendf(nil, "block-%04d", i)))
		}
		list.WriteString("</BlockList>")
		if c := put(t, up, "comp=blocklist", list.String()); c != http.StatusCreated {
			t.Fatalf("put block list: %d", c)
		}
	}
	res = rpc(t, srv, "FinalizeCacheEntryUpload", tok, map[string]any{"key": key, "version": version, "size_bytes": fmt.Sprint(size)})
	if res["ok"] != true {
		t.Fatalf("finalize %q: %v", key, res)
	}
}

// restore returns the matched key and content, or "" on a miss.
func restore(t *testing.T, srv *httptest.Server, tok, key, version string, restoreKeys ...string) (string, string) {
	t.Helper()
	res := rpc(t, srv, "GetCacheEntryDownloadURL", tok, map[string]any{"key": key, "restore_keys": restoreKeys, "version": version})
	if res["ok"] != true {
		return "", ""
	}
	resp, err := http.Get(res["signed_download_url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return res["matched_key"].(string), string(b)
}

func TestSaveAndRestore(t *testing.T) {
	srv := newServer(t, "http://unused", 0)
	save(t, srv, mainTok, "go-abc", "v1", "whole blob")
	save(t, srv, mainTok, "npm-1", "v1", "part0-", "part1-", "part2")

	if k, body := restore(t, srv, mainTok, "go-abc", "v1"); k != "go-abc" || body != "whole blob" {
		t.Errorf("put blob: got %q %q", k, body)
	}
	if _, body := restore(t, srv, mainTok, "npm-1", "v1"); body != "part0-part1-part2" {
		t.Errorf("blocks reassembled as %q, want them in block-list order", body)
	}
}

func TestLookupRules(t *testing.T) {
	srv := newServer(t, "http://unused", 0)
	save(t, srv, mainTok, "deps-old", "v1", "main old")
	time.Sleep(10 * time.Millisecond) // newest-first ordering needs distinct times
	save(t, srv, mainTok, "deps-new", "v1", "main new")
	save(t, srv, featureTok, "deps-feat", "v1", "feature")

	tests := []struct {
		name        string
		tok, key    string
		version     string
		restoreKeys []string
		wantKey     string
		wantBody    string
	}{
		{name: "exact", tok: mainTok, key: "deps-old", version: "v1", wantKey: "deps-old", wantBody: "main old"},
		{name: "restore key picks newest", tok: mainTok, key: "deps-zzz", version: "v1", restoreKeys: []string{"deps-"}, wantKey: "deps-new", wantBody: "main new"},
		{name: "key as prefix", tok: mainTok, key: "deps-n", version: "v1", wantKey: "deps-new"},
		{name: "own branch before default", tok: featureTok, key: "deps-x", version: "v1", restoreKeys: []string{"deps-"}, wantKey: "deps-feat", wantBody: "feature"},
		{name: "branch falls back to default", tok: featureTok, key: "deps-old", version: "v1", wantKey: "deps-old"},
		{name: "main can't see a branch's cache", tok: mainTok, key: "deps-feat", version: "v1"},
		{name: "version must match", tok: mainTok, key: "deps-old", version: "v2"},
		{name: "other repo misses", tok: otherRepo, key: "deps-old", version: "v1"},
		{name: "no match", tok: mainTok, key: "nope", version: "v1", restoreKeys: []string{"nada-"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k, body := restore(t, srv, tt.tok, tt.key, tt.version, tt.restoreKeys...)
			if k != tt.wantKey || (tt.wantBody != "" && body != tt.wantBody) {
				t.Errorf("got %q %q, want %q %q", k, body, tt.wantKey, tt.wantBody)
			}
		})
	}
}

func TestCreateRefusals(t *testing.T) {
	srv := newServer(t, "http://unused", 0)
	save(t, srv, mainTok, "k", "v1", "x")

	tests := []struct {
		name, tok, key, wantMsg string
		wantStatus              int
	}{
		{name: "entries are immutable", tok: mainTok, key: "k", wantMsg: "already exists", wantStatus: 200},
		{name: "a save in progress", tok: mainTok, key: "busy", wantMsg: "another job", wantStatus: 200},
		{name: "no token", tok: "garbage", key: "k2", wantStatus: 401},
		{name: "read-only scope", tok: token("1", "refs/heads/main:1"), key: "k3", wantMsg: "no writable scope", wantStatus: 200},
	}
	rpc(t, srv, "CreateCacheEntry", mainTok, map[string]string{"key": "busy", "version": "v1"})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := rpc(t, srv, "CreateCacheEntry", tt.tok, map[string]string{"key": tt.key, "version": "v1"})
			if res["status"] != tt.wantStatus || res["ok"] == true || !strings.Contains(fmt.Sprint(res["message"]), tt.wantMsg) {
				t.Errorf("got %v, want status %d and message containing %q", res, tt.wantStatus, tt.wantMsg)
			}
		})
	}
}

func TestUploadChecks(t *testing.T) {
	srv := newServer(t, "http://unused", 0)
	res := rpc(t, srv, "CreateCacheEntry", mainTok, map[string]string{"key": "k", "version": "v1"})
	up := res["signed_upload_url"].(string)
	bad := strings.Replace(up, "sig=", "sig=x", 1)
	if c := put(t, bad, "", "data"); c != http.StatusNotFound {
		t.Errorf("wrong signature: %d, want 404", c)
	}
	put(t, up, "", "four")
	res = rpc(t, srv, "FinalizeCacheEntryUpload", mainTok, map[string]any{"key": "k", "version": "v1", "size_bytes": 5})
	if res["ok"] == true || !strings.Contains(fmt.Sprint(res["message"]), "expected 5") {
		t.Errorf("size mismatch: %v", res)
	}
}

func TestRangeDownload(t *testing.T) {
	srv := newServer(t, "http://unused", 0)
	save(t, srv, mainTok, "k", "v1", "0123456789")
	res := rpc(t, srv, "GetCacheEntryDownloadURL", mainTok, map[string]any{"key": "k", "version": "v1"})
	req, _ := http.NewRequest(http.MethodGet, res["signed_download_url"].(string), nil)
	req.Header.Set("Range", "bytes=2-5")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusPartialContent || string(b) != "2345" {
		t.Errorf("range: %d %q", resp.StatusCode, b)
	}
}

func TestEviction(t *testing.T) {
	srv := newServer(t, "http://unused", 25)
	save(t, srv, mainTok, "a", "v1", "0123456789")
	time.Sleep(10 * time.Millisecond)
	save(t, srv, mainTok, "b", "v1", "0123456789")
	time.Sleep(10 * time.Millisecond)
	restore(t, srv, mainTok, "a", "v1") // a is now more recently used than b
	time.Sleep(10 * time.Millisecond)
	save(t, srv, mainTok, "c", "v1", "0123456789") // 30 bytes > 25: evict the LRU, b

	for key, want := range map[string]bool{"a": true, "b": false, "c": true} {
		if k, _ := restore(t, srv, mainTok, key, "v1"); (k != "") != want {
			t.Errorf("entry %q present = %v, want %v", key, k != "", want)
		}
	}
}

func TestPassthrough(t *testing.T) {
	var got *http.Request
	var gotBody string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer up.Close()
	srv := newServer(t, up.URL+"/", 0)

	path := "/twirp/github.actions.results.api.v1.ArtifactService/CreateArtifact"
	req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(`{"name":"a"}`))
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if got == nil || got.URL.Path != path || got.Header.Get("Authorization") != "Bearer secret" || gotBody != `{"name":"a"}` || string(b) != `{"ok":true}` {
		t.Errorf("forwarded %v body %q, response %q", got, gotBody, b)
	}
}

func TestWire(t *testing.T) {
	ids, err := blockIDs(strings.NewReader(`<?xml version="1.0"?><BlockList><Latest>a</Latest><Uncommitted> b </Uncommitted><Committed>c</Committed></BlockList>`))
	if err != nil || strings.Join(ids, ",") != "a,b,c" {
		t.Errorf("blockIDs = %q, %v", ids, err)
	}
	if _, err := blockIDs(strings.NewReader(`<BlockList></BlockList>`)); err == nil {
		t.Error("empty block list accepted")
	}
	for in, want := range map[string]int64{`123`: 123, `"456"`: 456} {
		var n int64JSON
		if err := json.Unmarshal([]byte(in), &n); err != nil || int64(n) != want {
			t.Errorf("int64JSON(%s) = %d, %v", in, n, err)
		}
	}
}
