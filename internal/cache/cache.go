// Package cache is a local stand-in for GitHub's Actions cache service (cache
// API v2). It runs inside the VM, so actions/cache, setup-* caches and BuildKit
// type=gha save and restore on the VM's disk instead of over the developer's
// internet link. Everything else the runner sends to the results service
// (artifacts) is forwarded to GitHub unchanged.
//
// It serves one developer's own jobs, so the job token is decoded but not
// verified: anything that can reach this server is already inside the VM with
// the token. Caches are still scoped by repository and branch the way GitHub
// scopes them, so a PR branch can't overwrite what main restores.
package cache

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// DefaultUpstream is GitHub's results service, which the runner would use
// without the patch.
const DefaultUpstream = "https://results-receiver.actions.githubusercontent.com/"

const twirpPrefix = "/twirp/github.actions.results.api.v1.CacheService/"

// staleUpload is how long an unfinalized upload is kept.
const staleUpload = time.Hour

// Permission bits in the token's cache scopes.
const (
	permRead  = 1
	permWrite = 2
)

type Server struct {
	Dir      string   // storage root: entries/ and uploads/
	MaxBytes int64    // evict least recently used entries above this; 0 means no limit
	Upstream *url.URL // where non-cache requests go
	Log      *slog.Logger

	mu    sync.Mutex // guards entry and upload metadata on disk
	proxy *httputil.ReverseProxy
}

// Handler returns the HTTP handler. Call it once.
func (s *Server) Handler() http.Handler {
	if s.Log == nil {
		s.Log = slog.New(slog.DiscardHandler)
	}
	up := s.Upstream
	s.proxy = &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.SetURL(up)
		r.Out.Host = up.Host
	}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+twirpPrefix+"CreateCacheEntry", s.create)
	mux.HandleFunc("POST "+twirpPrefix+"FinalizeCacheEntryUpload", s.finalize)
	mux.HandleFunc("POST "+twirpPrefix+"GetCacheEntryDownloadURL", s.lookup)
	mux.HandleFunc(twirpPrefix, func(w http.ResponseWriter, r *http.Request) {
		twirpError(w, http.StatusNotFound, "bad_route", "not implemented by the local cache")
	})
	mux.HandleFunc("PUT /upload/{id}", s.upload)
	mux.HandleFunc("GET /download/{ns}/{entry}", s.download)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok\n") })
	mux.Handle("/", s.proxy) // artifacts and anything else
	return mux
}

// --- token scopes ---

type scope struct {
	Name       string `json:"Scope"`
	Permission int    `json:"Permission"`
}

type claims struct {
	repo   string
	scopes []scope
}

// claimsOf decodes the job token's payload: repository_id, and ac (a JSON
// string listing the cache scopes, usually the branch first, then the default
// branch).
func claimsOf(r *http.Request) (claims, error) {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	parts := strings.Split(tok, ".")
	if !ok || len(parts) != 3 {
		return claims{}, errors.New("missing bearer token")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims{}, fmt.Errorf("token payload: %w", err)
	}
	var p struct {
		AC   string `json:"ac"`
		Repo string `json:"repository_id"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return claims{}, fmt.Errorf("token payload: %w", err)
	}
	var c claims
	if err := json.Unmarshal([]byte(p.AC), &c.scopes); err != nil || len(c.scopes) == 0 || p.Repo == "" {
		return claims{}, errors.New("token has no cache scopes or repository")
	}
	c.repo = p.Repo
	return c, nil
}

func (c claims) writeScope() (string, bool) {
	for _, s := range c.scopes {
		if s.Permission&permWrite != 0 {
			return s.Name, true
		}
	}
	return "", false
}

// readScopes lists readable scopes, the most privileged (the job's own branch) first.
func (c claims) readScopes() []string {
	var out []scope
	for _, s := range c.scopes {
		if s.Permission&permRead != 0 {
			out = append(out, s)
		}
	}
	slices.SortStableFunc(out, func(a, b scope) int { return b.Permission - a.Permission })
	names := make([]string, len(out))
	for i, s := range out {
		names[i] = s.Name
	}
	return names
}

// --- storage layout ---
//
//	entries/<ns>/<id>.json   entry metadata (key, size, created)
//	entries/<ns>/<id>.blob   the archive
//	entries/<ns>/<id>.used   touched on every restore, for LRU eviction
//	uploads/<id>/            meta.json, blocks/, data
//
// ns hashes (repository, scope, version) and id hashes the key, so paths never
// contain request data.

func hash(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:16])
}

func isHash(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil && len(s) == 32
}

type entryMeta struct {
	Key     string    `json:"key"`
	Size    int64     `json:"size"`
	Created time.Time `json:"created"`
}

type uploadMeta struct {
	NS, Key, Sig string
	Created      time.Time
}

func (s *Server) entryPath(ns, id, ext string) string {
	return filepath.Join(s.Dir, "entries", ns, id+ext)
}

func (s *Server) uploadDir(ns, key string) string {
	return filepath.Join(s.Dir, "uploads", hash(ns, key))
}

// --- twirp handlers ---

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v); err != nil {
		twirpError(w, http.StatusBadRequest, "malformed", err.Error())
		return false
	}
	return true
}

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func twirpError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "msg": msg})
}

func baseURL(r *http.Request) string { return "http://" + r.Host + "/" }

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	var req struct{ Key, Version string }
	if !decode(w, r, &req) {
		return
	}
	c, err := claimsOf(r)
	if err != nil {
		twirpError(w, http.StatusUnauthorized, "unauthenticated", err.Error())
		return
	}
	sc, ok := c.writeScope()
	if !ok || req.Key == "" || req.Version == "" {
		reply(w, map[string]any{"ok": false, "message": "no writable scope, key or version"})
		return
	}
	ns := hash(c.repo, sc, req.Version)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropStaleUploads()
	// Entries are immutable, as on GitHub: a second save of a key is refused.
	if _, err := os.Stat(s.entryPath(ns, hash(req.Key), ".json")); err == nil {
		reply(w, map[string]any{"ok": false, "message": "cache entry already exists"})
		return
	}
	dir := s.uploadDir(ns, req.Key)
	if m, err := readUpload(dir); err == nil && time.Since(m.Created) < staleUpload {
		reply(w, map[string]any{"ok": false, "message": "another job is saving this key"})
		return
	}
	_ = os.RemoveAll(dir)
	sig := rand.Text()
	if err := writeJSON(filepath.Join(dir, "meta.json"), uploadMeta{NS: ns, Key: req.Key, Sig: sig, Created: time.Now()}); err != nil {
		twirpError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	s.Log.Info("save started", "key", req.Key, "scope", sc)
	reply(w, map[string]any{"ok": true, "signed_upload_url": baseURL(r) + "upload/" + filepath.Base(dir) + "?sig=" + sig})
}

func (s *Server) finalize(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key       string
		Version   string
		SizeBytes int64JSON `json:"size_bytes"`
	}
	if !decode(w, r, &req) {
		return
	}
	c, err := claimsOf(r)
	if err != nil {
		twirpError(w, http.StatusUnauthorized, "unauthenticated", err.Error())
		return
	}
	sc, _ := c.writeScope()
	ns := hash(c.repo, sc, req.Version)
	dir := s.uploadDir(ns, req.Key)

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := readUpload(dir); err != nil {
		reply(w, map[string]any{"ok": false, "message": "no upload for this key"})
		return
	}
	fi, err := os.Stat(filepath.Join(dir, "data"))
	if err != nil {
		reply(w, map[string]any{"ok": false, "message": "nothing was uploaded"})
		return
	}
	if want := int64(req.SizeBytes); want > 0 && want != fi.Size() {
		reply(w, map[string]any{"ok": false, "message": fmt.Sprintf("uploaded %d bytes, expected %d", fi.Size(), want)})
		return
	}
	id := hash(req.Key)
	err = os.MkdirAll(filepath.Join(s.Dir, "entries", ns), 0o755)
	if err == nil {
		err = os.Rename(filepath.Join(dir, "data"), s.entryPath(ns, id, ".blob"))
	}
	if err == nil {
		// The metadata goes last: an entry is visible only once its blob is in place.
		err = writeJSON(s.entryPath(ns, id, ".json"), entryMeta{Key: req.Key, Size: fi.Size(), Created: time.Now()})
	}
	_ = os.RemoveAll(dir)
	if err != nil {
		twirpError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	s.Log.Info("saved", "key", req.Key, "scope", sc, "bytes", fi.Size())
	s.evict()
	reply(w, map[string]any{"ok": true, "entry_id": fmt.Sprint(time.Now().UnixNano())})
}

// lookup follows GitHub's matching order, per readable scope (own branch,
// then the default branch): the exact key, then the newest entry with the key
// as a prefix, then each restore key as a prefix.
func (s *Server) lookup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key         string
		RestoreKeys []string `json:"restore_keys"`
		Version     string
	}
	if !decode(w, r, &req) {
		return
	}
	c, err := claimsOf(r)
	if err != nil {
		twirpError(w, http.StatusUnauthorized, "unauthenticated", err.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sc := range c.readScopes() {
		ns := hash(c.repo, sc, req.Version)
		if _, err := os.Stat(s.entryPath(ns, hash(req.Key), ".json")); err == nil {
			s.hit(w, r, ns, req.Key)
			return
		}
		for _, prefix := range append([]string{req.Key}, req.RestoreKeys...) {
			if key, ok := s.newestWithPrefix(ns, prefix); ok {
				s.hit(w, r, ns, key)
				return
			}
		}
	}
	reply(w, map[string]any{"ok": false})
}

func (s *Server) hit(w http.ResponseWriter, r *http.Request, ns, key string) {
	id := hash(key)
	_ = touch(s.entryPath(ns, id, ".used"))
	s.Log.Info("restore hit", "key", key)
	reply(w, map[string]any{"ok": true, "signed_download_url": baseURL(r) + "download/" + ns + "/" + id, "matched_key": key})
}

func (s *Server) newestWithPrefix(ns, prefix string) (string, bool) {
	files, _ := filepath.Glob(filepath.Join(s.Dir, "entries", ns, "*.json"))
	var best entryMeta
	for _, f := range files {
		var m entryMeta
		if readJSON(f, &m) == nil && strings.HasPrefix(m.Key, prefix) && m.Created.After(best.Created) {
			best = m
		}
	}
	return best.Key, best.Key != ""
}

// --- blob transfer (the Azure Blob calls @actions/cache makes) ---

// upload accepts Put Blob (no comp), Put Block (comp=block&blockid=...) and
// Put Block List (comp=blocklist, an XML list of block ids in order).
func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	dir := filepath.Join(s.Dir, "uploads", r.PathValue("id"))
	m, err := readUpload(dir)
	if !isHash(r.PathValue("id")) || err != nil || r.URL.Query().Get("sig") != m.Sig {
		http.Error(w, "unknown upload", http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	switch q.Get("comp") {
	case "":
		err = writeFile(filepath.Join(dir, "data"), r.Body)
	case "block":
		id := q.Get("blockid")
		if id == "" {
			http.Error(w, "missing blockid", http.StatusBadRequest)
			return
		}
		err = writeFile(filepath.Join(dir, "blocks", hash(id)), r.Body)
	case "blocklist":
		err = s.commitBlocks(dir, r.Body)
	default:
		http.Error(w, "unsupported comp", http.StatusBadRequest)
		return
	}
	if err != nil {
		s.Log.Warn("upload failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("x-ms-request-id", rand.Text())
	w.WriteHeader(http.StatusCreated)
}

// commitBlocks concatenates the listed blocks, in the listed order, into data.
func (s *Server) commitBlocks(dir string, body io.Reader) error {
	ids, err := blockIDs(io.LimitReader(body, 64<<20))
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "data-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	for _, id := range ids {
		f, err := os.Open(filepath.Join(dir, "blocks", hash(id)))
		if err != nil {
			_ = tmp.Close()
			return fmt.Errorf("block %q: %w", id, err)
		}
		_, err = io.Copy(tmp, f)
		_ = f.Close()
		if err != nil {
			_ = tmp.Close()
			return err
		}
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, "data")); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(dir, "blocks"))
}

func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	ns, id := r.PathValue("ns"), r.PathValue("entry")
	if !isHash(ns) || !isHash(id) {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(s.entryPath(ns, id, ".blob"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, "", fi.ModTime(), f) // handles Range and HEAD
}

// --- housekeeping ---

// evict deletes least recently used entries until the total is under
// MaxBytes. Caller holds mu.
func (s *Server) evict() {
	if s.MaxBytes <= 0 {
		return
	}
	type entry struct {
		base string
		size int64
		used time.Time
	}
	var all []entry
	var total int64
	_ = filepath.WalkDir(filepath.Join(s.Dir, "entries"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || !strings.HasSuffix(p, ".blob") {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		base := strings.TrimSuffix(p, ".blob")
		used := fi.ModTime()
		if u, err := os.Stat(base + ".used"); err == nil && u.ModTime().After(used) {
			used = u.ModTime()
		}
		all = append(all, entry{base, fi.Size(), used})
		total += fi.Size()
		return nil
	})
	slices.SortFunc(all, func(a, b entry) int { return a.used.Compare(b.used) })
	for _, e := range all {
		if total <= s.MaxBytes {
			break
		}
		for _, ext := range []string{".json", ".blob", ".used"} {
			_ = os.Remove(e.base + ext)
		}
		total -= e.size
		s.Log.Info("evicted", "entry", filepath.Base(e.base), "bytes", e.size)
	}
}

// dropStaleUploads removes uploads that were never finalized. Caller holds mu.
func (s *Server) dropStaleUploads() {
	dirs, _ := os.ReadDir(filepath.Join(s.Dir, "uploads"))
	for _, d := range dirs {
		dir := filepath.Join(s.Dir, "uploads", d.Name())
		if m, err := readUpload(dir); err != nil || time.Since(m.Created) > staleUpload {
			_ = os.RemoveAll(dir)
		}
	}
}

// --- small file helpers ---

func readUpload(dir string) (uploadMeta, error) {
	var m uploadMeta
	return m, readJSON(filepath.Join(dir, "meta.json"), &m)
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func writeJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeFile(path, strings.NewReader(string(b)))
}

// writeFile writes atomically: a crash never leaves a half-written file.
func writeFile(path string, r io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func touch(path string) error {
	now := time.Now()
	err := os.Chtimes(path, now, now)
	if errors.Is(err, fs.ErrNotExist) {
		return writeFile(path, strings.NewReader(""))
	}
	return err
}
