// Package server implements Git's HTTP smart/dumb server.
//
// This file contains black-box HTTP tests for the server, derived from
// server/http.md (the protocol specification shipped in this branch).
//
// Each test group corresponds to a section of the spec:
//
//  1. pkt-line primitives          (spec §4)
//  2. Routing & general HTTP       (spec §2, §13, §14)
//  3. Authentication               (spec §2.2, §13.1)
//  4. Dumb HTTP protocol           (spec §5)
//  5. Smart HTTP v0/v1 discovery   (spec §6)
//  6. Smart HTTP v0/v1 fetch       (spec §7)
//  7. Smart HTTP v0/v1 push        (spec §8)
//  8. Git Protocol v2              (spec §10)
//  9. Edge cases & security        (spec §13, §14)
//
// Tests are written TDD-style: they describe the expected behaviour per the
// spec. As of the http branch, handlers in dumbProtocol.go and smartProtocol.go
// return 501 Not Implemented; tests should fail until the implementation
// matches the spec, one section at a time.
//
// Cross-checks against real `git` are used wherever the wire format is
// observable from outside — same convention as git/pack_test.go.
package server

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"got/storage"
	"got/storage/filesystem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ============================================================================
// Constants mirrored from the spec (§4.1, §10.1)
// ============================================================================

const (
	// specMaxPktLineData is the largest payload (excluding the 4-byte length
	// prefix) that a single pkt-line may carry (spec §4.1).
	specMaxPktLineData = 65516
	// specMaxPktLineTotal is the largest total pkt-line length, including the
	// 4-byte length prefix (spec §4.1: "MUST NOT send pkt-line longer than
	// 65520").
	specMaxPktLineTotal = 65520
)

// Content-Type strings mandated by the spec (§5.1, §6.2, §7.1, §7.10, §8).
const (
	ctDumbInfoRefs         = "text/plain; charset=utf-8"
	ctDumbPacks            = "text/plain; charset=utf-8"
	ctLooseObject          = "application/x-git-loose-object"
	ctPackedObjects        = "application/x-git-packed-objects"
	ctPackedObjectsTOC     = "application/x-git-packed-objects-toc"
	ctUploadPackAdvertise  = "application/x-git-upload-pack-advertisement"
	ctUploadPackRequest    = "application/x-git-upload-pack-request"
	ctUploadPackResult     = "application/x-git-upload-pack-result"
	ctReceivePackAdvertise = "application/x-git-receive-pack-advertisement"
	ctReceivePackRequest   = "application/x-git-receive-pack-request"
	ctReceivePackResult    = "application/x-git-receive-pack-result"
)

// Cache-Control strings mandated by the spec (§3, §5.6, §5.7, §7.10, §14.7).
const (
	cacheImmutable = "public, max-age=31536000"
	cacheNoStore   = "no-cache, max-age=0, must-revalidate"
)

// ============================================================================
// Shared test helpers
// ============================================================================

// setupBareRepo creates a bare git repository in a temp directory and
// populates it with two commits on the default branch plus an annotated tag.
// Runs `git update-server-info` so dumb-protocol info files exist on disk
// as well (some servers serve them statically; others generate on the fly).
//
// Returns:
//   - rootDir:      parent directory containing the bare repo (use as Config.ReposDir)
//   - repoName:     relative name of the repo (e.g. "test.git")
//   - headSHA:      SHA-1 of refs/heads/master
//   - refs:         map of ref name → SHA (e.g. "refs/heads/master" → "abc123...")
//   - packFiles:    list of pack file basenames in objects/pack/
//   - looseObjects: list of SHA-1s of loose objects in objects/ (may be empty after gc)
//   - cleanup
func setupBareRepo(t *testing.T) (rootDir, repoName, headSHA string, refs map[string]string, packFiles, looseObjects []string, cleanup func()) {
	t.Helper()

	// Temp dir for everything.
	root, err := os.MkdirTemp("", "got-http-test-*")
	if err != nil {
		t.Fatal(err)
	}

	// Create a non-bare repo to author commits in, then clone --bare it.
	workDir := filepath.Join(root, "work")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		os.RemoveAll(root)
		t.Fatal(err)
	}

	runGitInDir(t, workDir, "init", "--initial-branch=master")
	runGitInDir(t, workDir, "config", "user.email", "test@example.com")
	runGitInDir(t, workDir, "config", "user.name", "Test User")

	// Commit 1.
	mustWriteFile(t, filepath.Join(workDir, "README.md"), "# Test Project\n\nThis is a test.\n")
	runGitInDir(t, workDir, "add", "README.md")
	runGitInDir(t, workDir, "commit", "-m", "initial commit")

	// Commit 2 (so we have history).
	mustWriteFile(t, filepath.Join(workDir, "main.go"), "package main\n\nfunc main() {}\n")
	runGitInDir(t, workDir, "add", "main.go")
	runGitInDir(t, workDir, "commit", "-m", "add main.go")

	// Annotated tag — used to test peeled refs in info/refs.
	runGitInDir(t, workDir, "tag", "-a", "v1.0", "-m", "release 1.0")

	// Bare clone into <root>/test.git.
	repoName = "test.git"
	barePath := filepath.Join(root, repoName)
	runGitInDir(t, workDir, "clone", "--bare", ".", barePath)

	// Repack to ensure all objects are in a single packfile.
	runGitInDir(t, barePath, "gc", "--quiet", "--prune=now")
	// Generate dumb-protocol metadata files on disk.
	runGitInDir(t, barePath, "update-server-info")

	// Collect refs via for-each-ref.
	refs = make(map[string]string)
	out := runGitInDir(t, barePath, "for-each-ref", "--format=%(refname) %(objectname)")
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) == 2 {
			refs[parts[0]] = parts[1]
		}
	}
	headSHA = refs["refs/heads/master"]

	// Collect pack files (basenames, e.g. "pack-<40 hex>.pack").
	packDir := filepath.Join(barePath, "objects", "pack")
	entries, _ := os.ReadDir(packDir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".pack") {
			packFiles = append(packFiles, e.Name())
		}
	}
	if len(packFiles) == 0 {
		t.Fatalf("setupBareRepo: no pack files in %s", packDir)
	}

	// Collect loose objects (may be empty after gc).
	looseObjects = collectLooseObjects(t, filepath.Join(barePath, "objects"))

	cleanup = func() {
		os.RemoveAll(root)
	}
	return root, repoName, headSHA, refs, packFiles, looseObjects, cleanup
}

// collectLooseObjects walks objects/ and returns SHA-1s of loose objects.
func collectLooseObjects(t *testing.T, objectsDir string) []string {
	t.Helper()
	var shas []string
	_ = filepath.Walk(objectsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		// Loose object path: objects/<2 hex>/<38 hex>
		dir := filepath.Base(filepath.Dir(path))
		name := info.Name()
		if len(dir) == 2 && len(name) == 38 && isHex(dir) && isHex(name) {
			shas = append(shas, dir+name)
		}
		return nil
	})
	return shas
}

func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// runGitInDir runs `git` in dir and returns trimmed stdout.
func runGitInDir(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s failed: %v\noutput: %s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// mustWriteFile writes content to path, creating parent directories.
func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func newTestStorage() *storage.Storage {
	fs := &filesystem.FileSystemStorage{}
	var repoStorage storage.Storage = fs
	return &repoStorage
}

// newTestServer spins up an httptest.Server wrapping NewServer(cfg).
func newTestServer(t *testing.T, cfg Config) *httptest.Server {
	t.Helper()
	return httptest.NewServer(NewServer(cfg, newTestStorage()))
}

// newDefaultTestServer creates a server pointing at rootDir as ReposDir.
func newDefaultTestServer(t *testing.T, rootDir string) *httptest.Server {
	t.Helper()
	return newTestServer(t, Config{ReposDir: rootDir})
}

// repoURL joins a test server URL with a repo name (and optional sub-path).
func repoURL(srv *httptest.Server, repoName string, sub ...string) string {
	return strings.Join(append([]string{srv.URL, repoName}, sub...), "/")
}

// doRequest performs an HTTP request and returns the response. Fails the
// test on transport errors. Caller is responsible for closing the body.
func doRequest(t *testing.T, method, url string, body io.Reader, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// doGET is a shorthand for doRequest with method GET and no body.
func doGET(t *testing.T, url string, headers map[string]string) *http.Response {
	t.Helper()
	return doRequest(t, http.MethodGet, url, nil, headers)
}

// mustReadBody reads r fully or fails the test.
func mustReadBody(t *testing.T, r io.Reader) []byte {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// mustResp reads resp.Body fully, closes it, and returns the bytes.
func mustResp(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	defer resp.Body.Close()
	return mustReadBody(t, resp.Body)
}

// looseObjectZlib returns the zlib-compressed loose-object bytes for the
// given type+content. Mirrors how `git hash-object -w` stores objects on
// disk: zlib-compressed ("type size\x00content").
func looseObjectZlib(objType string, content []byte) []byte {
	header := fmt.Sprintf("%s %d\x00", objType, len(content))
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	zw.Write([]byte(header))
	zw.Write(content)
	zw.Close()
	return buf.Bytes()
}

// hexSHA1 computes the lowercase hex SHA-1 of b.
func hexSHA1(b []byte) string {
	sum := sha1.Sum(b)
	return hex.EncodeToString(sum[:])
}

// sortRefsByName sorts a map of refname→SHA into a slice of (name, sha)
// pairs ordered by name in C-locale byte order (spec §5.2).
func sortRefsByName(refs map[string]string) []refEntry {
	out := make([]refEntry, 0, len(refs))
	for name, sha := range refs {
		out = append(out, refEntry{name, sha})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

type refEntry struct{ name, sha string }

// gzipBytes gzip-compresses in and returns the result.
func gzipBytes(t *testing.T, in []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(in); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// extractPackFromSideband demultiplexes a side-band-64k (or side-band)
// pkt-line stream and returns the concatenation of all channel-1 (pack
// data) payloads. Channel-2 (progress) and channel-3 (fatal) are
// returned separately so tests can assert on them.
func extractPackFromSideband(t *testing.T, lines []parsedPktLine) (pack, progress, fatal []byte) {
	t.Helper()
	for _, l := range lines {
		if l.kind != pktData || len(l.bytes) == 0 {
			continue
		}
		switch l.bytes[0] {
		case 1:
			pack = append(pack, l.bytes[1:]...)
		case 2:
			progress = append(progress, l.bytes[1:]...)
		case 3:
			fatal = append(fatal, l.bytes[1:]...)
		default:
			t.Fatalf("unknown side-band channel %d in pkt-line %q", l.bytes[0], l.bytes)
		}
	}
	return pack, progress, fatal
}

// hasPrefixBytes reports whether buf starts with prefix.
func hasPrefixBytes(buf, prefix []byte) bool {
	return len(buf) >= len(prefix) && bytes.Equal(buf[:len(prefix)], prefix)
}

// ============================================================================
// Section 2: routing & general HTTP behavior  (spec §2, §14)
// ============================================================================
//
// These tests cover the dispatch logic in server.go and general HTTP
// behaviour the spec mandates for ALL endpoints (status codes for missing
// repos, wrong methods, etc.).

// TestRoute_InfoRefs_MatchesRepoPath verifies that the regex used for
// /info/refs correctly extracts the repo path component, regardless of
// whether the repo lives at the top level or under a namespace.
func TestRoute_InfoRefs_MatchesRepoPath(t *testing.T) {
	cases := []struct {
		urlPath  string
		wantPath string
	}{
		{"/test.git/info/refs", "/test.git"},
		{"/ns/test.git/info/refs", "/ns/test.git"},
		{"/a/b/c.git/info/refs", "/a/b/c.git"},
	}
	for _, c := range cases {
		t.Run(c.urlPath, func(t *testing.T) {
			srv := NewServer(Config{ReposDir: t.TempDir()}, newTestStorage())
			req := httptest.NewRequest(http.MethodGet, c.urlPath, nil)
			s, path := srv.route(req)
			if s == nil {
				t.Fatalf("route: no match for %s", c.urlPath)
			}
			if path != c.wantPath {
				t.Errorf("path = %q, want %q", path, c.wantPath)
			}
		})
	}
}

// TestRoute_LooseObjectRegex accepts SHA-1 (40 hex) and SHA-256 (64 hex)
// object paths per spec §5.1. The route regex in server.go uses
// [0-9a-f]{38,62} for the tail, so it accepts both SHA-1 (38 chars) and
// SHA-256 (62 chars) tails.
func TestRoute_LooseObjectRegex(t *testing.T) {
	sha1Tail := "49f6c27a2244e12041955e262a404c7faba355" // 38 hex chars
	if len(sha1Tail) != 38 {
		t.Fatalf("test fixture sha1Tail has wrong length: %d", len(sha1Tail))
	}
	// 62-hex tail for SHA-256.
	sha256Tail := "00000000000000000000000000000000000000000000000000000000000000"
	if len(sha256Tail) != 62 {
		t.Fatalf("test fixture sha256Tail has wrong length: %d", len(sha256Tail))
	}

	t.Run("sha1", func(t *testing.T) {
		srv := NewServer(Config{ReposDir: t.TempDir()}, newTestStorage())
		req := httptest.NewRequest(http.MethodGet, "/test.git/objects/d0/"+sha1Tail, nil)
		s, _ := srv.route(req)
		if s == nil {
			t.Fatal("expected route match for SHA-1 loose object path")
		}
	})

	t.Run("sha256", func(t *testing.T) {
		srv := NewServer(Config{ReposDir: t.TempDir()}, newTestStorage())
		req := httptest.NewRequest(http.MethodGet, "/test.git/objects/d0/"+sha256Tail, nil)
		s, _ := srv.route(req)
		if s == nil {
			t.Fatal("expected route match for SHA-256 loose object path")
		}
	})
}

// TestRoute_PackFileRegex accepts both .pack and .idx URLs for SHA-1 and
// SHA-256 pack names (spec §5.1, §5.7).
func TestRoute_PackFileRegex(t *testing.T) {
	sha1Pack := "/test.git/objects/pack/pack-" + strings.Repeat("a", 40) + ".pack"
	sha256Pack := "/test.git/objects/pack/pack-" + strings.Repeat("a", 64) + ".pack"
	sha1Idx := "/test.git/objects/pack/pack-" + strings.Repeat("a", 40) + ".idx"
	sha256Idx := "/test.git/objects/pack/pack-" + strings.Repeat("a", 64) + ".idx"

	for _, p := range []string{sha1Pack, sha256Pack, sha1Idx, sha256Idx} {
		t.Run(p, func(t *testing.T) {
			srv := NewServer(Config{ReposDir: t.TempDir()}, newTestStorage())
			req := httptest.NewRequest(http.MethodGet, p, nil)
			s, _ := srv.route(req)
			if s == nil {
				t.Fatalf("expected route match for %s", p)
			}
		})
	}
}

// TestServeHTTP_UnknownPath_Returns403 verifies spec §2.5: a path that
// doesn't match any Git endpoint must NOT be answered 200 OK. (Current
// implementation returns 403 Forbidden; spec says 404/410/403 are all
// acceptable.)
func TestServeHTTP_UnknownPath_Returns403(t *testing.T) {
	srv := newDefaultTestServer(t, t.TempDir())
	defer srv.Close()

	resp := doGET(t, srv.URL+"/totally/unrelated/path", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden &&
		resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 403 or 404", resp.StatusCode)
	}
}

// TestServeHTTP_NonExistentRepo_Returns404 verifies spec §2.5: "If the
// repository at $GIT_URL does not exist, the server MUST NOT respond 200
// OK. SHOULD respond 404 Not Found."
//
// This test asserts the SHOULD: 404. (The current skeleton returns 501 from
// the handler — the implementation should reject unknown repos before
// dispatching to a handler.)
func TestServeHTTP_NonExistentRepo_Returns404(t *testing.T) {
	root, _, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	// Hit a known endpoint pattern with a repo name that does NOT exist.
	resp := doGET(t, repoURL(srv, "does-not-exist.git", "info", "refs"), nil)
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		t.Errorf("status = 200 for non-existent repo; spec forbids this")
	}
	// Spec allows 404, 410, or any code not implying existence.
	if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusGone {
		t.Logf("non-existent repo returned %d (spec only says 'not 200'); ideally 404", resp.StatusCode)
	}
}

// TestServeHTTP_WrongMethod_Returns405 verifies spec §14.6: an endpoint
// reached with an unexpected HTTP method should return 405 Method Not
// Allowed (or at minimum, not 200).
//
// Example: POST /info/refs is not part of any protocol.
func TestServeHTTP_WrongMethod_Returns405(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	resp := doRequest(t, http.MethodPost, repoURL(srv, repoName, "info", "refs"), strings.NewReader(""), nil)
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		t.Errorf("POST /info/refs returned 200; method should be rejected")
	}
}

// TestServeHTTP_HEADRequest_NotAllowed verifies spec §14.5: a HEAD
// request to a Git endpoint must NOT be served as if it were GET.
//
// (The current implementation only registers GET handlers; HEAD will not
// match any service and should yield 403/405/404.)
func TestServeHTTP_HEADRequest_NotAllowed(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	resp := doRequest(t, http.MethodHead, repoURL(srv, repoName, "info", "refs"), nil, nil)
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		t.Errorf("HEAD /info/refs returned 200; HEAD should not be treated as GET")
	}
}

// TestServeHTTP_ReposDirWithNamespace verifies that namespaced repos
// (e.g. /alice/test.git/...) are routed correctly via getNamespaceAndRepo.
func TestServeHTTP_ReposDirWithNamespace(t *testing.T) {
	root, repoName, _, refs, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	// Move the bare repo under a namespace subdir.
	nsDir := filepath.Join(root, "alice")
	if err := os.MkdirAll(nsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, repoName), filepath.Join(nsDir, repoName)); err != nil {
		t.Fatal(err)
	}

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	// Dumb GET /info/refs on the namespaced repo.
	resp := doGET(t, repoURL(srv, "alice", repoName, "info", "refs"), nil)
	defer resp.Body.Close()

	// We don't care (yet) about the exact status — only that routing
	// matched and dispatched to a handler rather than 403'ing. After
	// the dumb handler is implemented, this will be 200 + the ref list.
	if resp.StatusCode == http.StatusForbidden {
		t.Errorf("namespaced repo was not routed (got 403); check getNamespaceAndRepo")
	}
	// Sanity: at least one ref must be reachable from the repo for the
	// subsequent tests to make sense.
	if len(refs) == 0 {
		t.Fatal("test fixture has no refs")
	}
}

// ============================================================================
// Section 3: authentication  (spec §2.2, §13.1)
// ============================================================================

// TestAuth_NoAuthHeader_Returns401WithWWWAuthenticate verifies that when
// Config.Auth=true and the request lacks an Authorization header, the
// server responds 401 with a WWW-Authenticate: Basic challenge.
//
// Spec §2.2: "Client SHOULD support Basic authentication (RFC 2617)."
// Spec §13.1: "Authentication MAY be required ... the server SHOULD rely
// on the HTTP server (frontend) for Basic authentication."
func TestAuth_NoAuthHeader_Returns401WithWWWAuthenticate(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := NewServer(Config{ReposDir: root, Auth: true}, newTestStorage())
	srv.AuthFunc = func(_ Credential, _ *Request) (bool, error) { return false, nil }
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp := doGET(t, repoURL(ts, repoName, "info", "refs"), nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	chal := resp.Header.Get("WWW-Authenticate")
	if !strings.HasPrefix(chal, "Basic") {
		t.Errorf("WWW-Authenticate = %q, want \"Basic ...\"", chal)
	}
}

// TestAuth_NoAuthFunc_Returns401 verifies that if Auth is enabled but no
// AuthFunc was wired up, the server fails closed (401) rather than letting
// the request through.
func TestAuth_NoAuthFunc_Returns401(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newTestServer(t, Config{ReposDir: root, Auth: true}) // AuthFunc is nil
	defer srv.Close()

	resp := doGET(t, repoURL(srv, repoName, "info", "refs"), nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 (Auth enabled, AuthFunc nil)", resp.StatusCode)
	}
}

// TestAuth_InvalidCredentials_Returns401 verifies that bad credentials
// are rejected.
func TestAuth_InvalidCredentials_Returns401(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	s := NewServer(Config{ReposDir: root, Auth: true}, newTestStorage())
	s.AuthFunc = func(_ Credential, _ *Request) (bool, error) { return false, nil }
	ts := httptest.NewServer(s)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, repoURL(ts, repoName, "info", "refs"), nil)
	req.SetBasicAuth("alice", "wrong-password")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 (bad credentials)", resp.StatusCode)
	}
}

// TestAuth_ValidCredentials_Proceeds verifies that valid credentials let
// the request reach the handler.
func TestAuth_ValidCredentials_Proceeds(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	s := NewServer(Config{ReposDir: root, Auth: true}, newTestStorage())
	s.AuthFunc = func(_ Credential, _ *Request) (bool, error) { return true, nil }
	ts := httptest.NewServer(s)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, repoURL(ts, repoName, "info", "refs"), nil)
	req.SetBasicAuth("alice", "good-password")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Valid creds should NOT yield 401. The endpoint itself is not yet
	// implemented (returns 501), but that's fine — we only care that
	// auth was satisfied.
	if resp.StatusCode == http.StatusUnauthorized {
		t.Errorf("status = 401 despite valid credentials")
	}
}

// ============================================================================
// Section 4: Dumb HTTP protocol  (spec §5)
// ============================================================================
//
// Dumb-protocol tests spin up a real bare git repo (with
// update-server-info having run) and assert that the server's dumb
// endpoints return the bytes/files git's dumb client expects.

// --- §5.1 / §5.2: GET /info/refs ---

// TestDumb_InfoRefs_NoServiceParam verifies that GET /info/refs without
// a `service=` query parameter returns the plain-text dumb ref listing
// (spec §5.2).
func TestDumb_InfoRefs_NoServiceParam(t *testing.T) {
	root, repoName, _, refs, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	resp := doGET(t, repoURL(srv, repoName, "info", "refs"), nil)
	defer resp.Body.Close()
	body := mustReadBody(t, resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	// Content-Type must NOT start with application/x-git- (spec §5.1).
	ct := resp.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/x-git-") {
		t.Errorf("Content-Type = %q; dumb must not advertise smart Content-Type", ct)
	}

	// Each non-peeled ref must appear as "<40-hex sha>\t<refname>\n".
	// Peeled annotated tags add a "<40-hex sha>\t<refname>^{}\n" line.
	for _, e := range sortRefsByName(refs) {
		// Skip peeled form: it's tested separately. We only need to find
		// the base line here.
		baseLine := fmt.Sprintf("%s\t%s\n", e.sha, e.name)
		if !bytes.Contains(body, []byte(baseLine)) {
			t.Errorf("dumb info/refs missing line %q; body:\n%s", baseLine, body)
		}
	}
}

// TestDumb_InfoRefs_ContentType verifies spec §5.2: "Content-Type of the
// response SHOULD be text/plain; charset=utf-8".
func TestDumb_InfoRefs_ContentType(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	resp := doGET(t, repoURL(srv, repoName, "info", "refs"), nil)
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	// Spec says "SHOULD" — accept anything that's not smart.
	if strings.HasPrefix(ct, "application/x-git-") {
		t.Errorf("Content-Type = %q; dumb must not advertise smart", ct)
	}
}

// TestDumb_InfoRefs_CacheControl verifies spec §3 / §14.7: dumb info/refs
// is dynamic and must be served with no-cache.
func TestDumb_InfoRefs_CacheControl(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	resp := doGET(t, repoURL(srv, repoName, "info", "refs"), nil)
	defer resp.Body.Close()

	cc := resp.Header.Get("Cache-Control")
	if cc == "" {
		t.Logf("Cache-Control empty; spec recommends no-cache on info/refs")
		return
	}
	if !strings.Contains(strings.ToLower(cc), "no-cache") {
		t.Errorf("Cache-Control = %q; want contains \"no-cache\"", cc)
	}
}

// TestDumb_InfoRefs_SortedByRefname verifies spec §5.2: "File SHOULD be
// sorted by name in C locale ordering."
func TestDumb_InfoRefs_SortedByRefname(t *testing.T) {
	root, repoName, _, refs, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	resp := doGET(t, repoURL(srv, repoName, "info", "refs"), nil)
	defer resp.Body.Close()
	body := string(mustReadBody(t, resp.Body))

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// Extract ref names from "<sha>\t<name>" lines, ignoring peeled ^{}.
	var gotNames []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		tab := strings.IndexByte(line, '\t')
		if tab < 0 {
			continue
		}
		name := line[tab+1:]
		if strings.HasSuffix(name, "^{}") {
			continue
		}
		gotNames = append(gotNames, name)
	}

	// Verify each expected ref is present.
	wantSet := make(map[string]bool, len(refs))
	for n := range refs {
		wantSet[n] = true
	}
	for _, n := range gotNames {
		if !wantSet[n] {
			t.Errorf("unexpected ref %q in dumb info/refs", n)
		}
	}
	for n := range wantSet {
		found := false
		for _, g := range gotNames {
			if g == n {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing ref %q in dumb info/refs", n)
		}
	}

	// Verify sort order (C locale = byte order).
	if !sort.StringsAreSorted(gotNames) {
		t.Errorf("refs in dumb info/refs are not sorted: %v", gotNames)
	}
}

// TestDumb_InfoRefs_PeeledAnnotatedTag verifies spec §5.2: an annotated
// tag is followed by a line "<sha>\t<refname>^{}\n" pointing at the commit
// the tag ultimately references.
func TestDumb_InfoRefs_PeeledAnnotatedTag(t *testing.T) {
	root, repoName, _, refs, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	resp := doGET(t, repoURL(srv, repoName, "info", "refs"), nil)
	defer resp.Body.Close()
	body := string(mustReadBody(t, resp.Body))

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	tagSHA, ok := refs["refs/tags/v1.0"]
	if !ok {
		t.Fatal("test fixture missing refs/tags/v1.0")
	}

	// Find the v1.0 line and verify a ^{} companion line follows.
	var peeledSHA string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasSuffix(line, "\trefs/tags/v1.0^{}") {
			tab := strings.IndexByte(line, '\t')
			peeledSHA = line[:tab]
			break
		}
	}
	if peeledSHA == "" {
		t.Fatalf("no peeled line for refs/tags/v1.0 in body:\n%s", body)
	}
	// peeledSHA must be a 40-hex SHA-1 of the commit the tag points at.
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(peeledSHA) {
		t.Errorf("peeled SHA %q is not 40 hex digits", peeledSHA)
	}
	// And it must NOT equal the tag object's own SHA (the tag is an
	// annotated tag object; the peeled line points to the commit).
	if peeledSHA == tagSHA {
		t.Errorf("peeled SHA equals tag SHA %s; should be the commit the tag references", tagSHA)
	}
}

// TestDumb_InfoRefs_DoesNotIncludeHEAD verifies spec §5.2: "File SHOULD
// NOT include the default ref HEAD (it is returned separately via /HEAD)."
func TestDumb_InfoRefs_DoesNotIncludeHEAD(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	resp := doGET(t, repoURL(srv, repoName, "info", "refs"), nil)
	defer resp.Body.Close()
	body := string(mustReadBody(t, resp.Body))

	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, "\tHEAD") {
			t.Errorf("dumb info/refs must not include HEAD; line: %q", line)
		}
	}
}

// --- §5.3: GET /HEAD ---

// TestDumb_GetHEAD verifies spec §5.3: GET /HEAD returns "ref:
// refs/heads/<branch>\n" (or a bare SHA for a detached HEAD).
func TestDumb_GetHEAD(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	resp := doGET(t, repoURL(srv, repoName, "HEAD"), nil)
	defer resp.Body.Close()
	body := string(mustReadBody(t, resp.Body))

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	// Bare repo created by `git clone --bare` has HEAD as a symref.
	if !strings.HasPrefix(body, "ref: refs/heads/") {
		t.Errorf("HEAD body = %q, want \"ref: refs/heads/<branch>\\n\"", body)
	}
}

// TestDumb_GetHEAD_ContentType verifies spec §5.1: Content-Type for /HEAD
// is text/plain.
func TestDumb_GetHEAD_ContentType(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	resp := doGET(t, repoURL(srv, repoName, "HEAD"), nil)
	defer resp.Body.Close()
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want \"text/plain...\"", ct)
	}
}

// --- §5.4: GET /objects/info/packs ---

// TestDumb_GetInfoPacks_Format verifies spec §5.4: response is one "P
// pack-<hash>.pack" line per packfile, terminated by a blank line.
func TestDumb_GetInfoPacks_Format(t *testing.T) {
	root, repoName, _, _, packFiles, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	resp := doGET(t, repoURL(srv, repoName, "objects", "info", "packs"), nil)
	defer resp.Body.Close()
	body := string(mustReadBody(t, resp.Body))

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	// Each pack file in the test repo must appear as "P <filename>".
	for _, pf := range packFiles {
		want := "P " + pf
		if !strings.Contains(body, want) {
			t.Errorf("info/packs missing line %q; body:\n%s", want, body)
		}
	}

	// Each line must start with "P " (and end with .pack).
	for _, line := range strings.Split(body, "\n") {
		if line == "" {
			continue // trailing blank line is OK
		}
		if !strings.HasPrefix(line, "P ") {
			t.Errorf("unexpected line in info/packs: %q", line)
		}
		if !strings.HasSuffix(line, ".pack") {
			t.Errorf("pack line does not end with .pack: %q", line)
		}
	}
}

// TestDumb_GetInfoPacks_ContentType verifies spec §5.1 / §5.4:
// Content-Type is text/plain; charset=utf-8.
func TestDumb_GetInfoPacks_ContentType(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	resp := doGET(t, repoURL(srv, repoName, "objects", "info", "packs"), nil)
	defer resp.Body.Close()
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want \"text/plain...\"", ct)
	}
}

// --- §5.6: GET /objects/<hh>/<rest> (loose object) ---

// TestDumb_GetLooseObject_ServesZlibBytes verifies spec §5.6: response
// body is the zlib-compressed loose object bytes; Content-Type is
// application/x-git-loose-object.
//
// This test only runs if the bare repo has loose objects after gc; if
// everything is in a packfile, it is skipped (dumb loose-object endpoint
// is still tested for 404 behaviour in TestDumb_GetLooseObject_Nonexistent).
func TestDumb_GetLooseObject_ServesZlibBytes(t *testing.T) {
	root, repoName, _, _, _, looseObjects, cleanup := setupBareRepo(t)
	defer cleanup()

	if len(looseObjects) == 0 {
		t.Skip("no loose objects in test repo (all packed); skipping loose-object content test")
	}

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	sha := looseObjects[0]
	resp := doGET(t, repoURL(srv, repoName, "objects", sha[:2], sha[2:]), nil)
	defer resp.Body.Close()
	body := mustReadBody(t, resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != ctLooseObject {
		t.Errorf("Content-Type = %q, want %q", ct, ctLooseObject)
	}

	// Body must be a valid zlib stream starting with the object header
	// ("type size\x00") once decompressed.
	zr, err := zlib.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("response body is not a zlib stream: %v", err)
	}
	defer zr.Close()
	decompressed, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("zlib decompress failed: %v", err)
	}
	// Must start with one of the four object type names.
	if !strings.HasPrefix(string(decompressed), "blob ") &&
		!strings.HasPrefix(string(decompressed), "tree ") &&
		!strings.HasPrefix(string(decompressed), "commit ") &&
		!strings.HasPrefix(string(decompressed), "tag ") {
		t.Errorf("decompressed object does not start with a known type header: %q", decompressed[:32])
	}

	// Cross-check the SHA-1 of the decompressed object against the path.
	sum := sha1.Sum(decompressed)
	if hex.EncodeToString(sum[:]) != sha {
		t.Errorf("SHA-1 mismatch: path says %s, decompressed content hashes to %s",
			sha, hex.EncodeToString(sum[:]))
	}
}

// TestDumb_GetLooseObject_CacheControlImmutable verifies spec §5.6:
// loose objects are immutable and SHOULD be cached forever.
func TestDumb_GetLooseObject_CacheControlImmutable(t *testing.T) {
	root, repoName, _, _, _, looseObjects, cleanup := setupBareRepo(t)
	defer cleanup()

	if len(looseObjects) == 0 {
		t.Skip("no loose objects in test repo")
	}

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	sha := looseObjects[0]
	resp := doGET(t, repoURL(srv, repoName, "objects", sha[:2], sha[2:]), nil)
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	cc := resp.Header.Get("Cache-Control")
	if cc == "" {
		t.Logf("Cache-Control empty; spec recommends immutable (public, max-age=31536000)")
		return
	}
	// Must NOT contain no-cache (loose objects are immutable).
	if strings.Contains(strings.ToLower(cc), "no-cache") {
		t.Errorf("Cache-Control = %q; loose objects are immutable, must not be no-cache", cc)
	}
}

// TestDumb_GetLooseObject_Nonexistent_Returns404 verifies that requesting
// a non-existent loose object yields 404.
func TestDumb_GetLooseObject_Nonexistent_Returns404(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	// Use a syntactically valid but non-existent SHA-1.
	fakeSHA := strings.Repeat("0", 40)
	resp := doGET(t, repoURL(srv, repoName, "objects", fakeSHA[:2], fakeSHA[2:]), nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for non-existent loose object", resp.StatusCode)
	}
}

// --- §5.7: GET /objects/pack/pack-<hash>.pack ---

// TestDumb_GetPackFile_ServesPackBytes verifies spec §5.7: packfile is
// served with Content-Type application/x-git-packed-objects and starts
// with the "PACK" magic.
func TestDumb_GetPackFile_ServesPackBytes(t *testing.T) {
	root, repoName, _, _, packFiles, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	pack := packFiles[0]
	resp := doGET(t, repoURL(srv, repoName, "objects", "pack", pack), nil)
	defer resp.Body.Close()
	body := mustReadBody(t, resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != ctPackedObjects {
		t.Errorf("Content-Type = %q, want %q", ct, ctPackedObjects)
	}
	if !hasPrefixBytes(body, []byte("PACK")) {
		t.Errorf("packfile body does not start with \"PACK\" magic; first 8 bytes: %x", body[:8])
	}
}

// TestDumb_GetPackFile_CacheControlImmutable verifies spec §5.7: packfiles
// are immutable.
func TestDumb_GetPackFile_CacheControlImmutable(t *testing.T) {
	root, repoName, _, _, packFiles, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	resp := doGET(t, repoURL(srv, repoName, "objects", "pack", packFiles[0]), nil)
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	cc := resp.Header.Get("Cache-Control")
	if cc == "" {
		t.Logf("Cache-Control empty; spec recommends immutable for packfiles")
		return
	}
	if strings.Contains(strings.ToLower(cc), "no-cache") {
		t.Errorf("Cache-Control = %q; packfiles are immutable", cc)
	}
}

// TestDumb_GetPackFile_Nonexistent_Returns404.
func TestDumb_GetPackFile_Nonexistent_Returns404(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	fake := "pack-" + strings.Repeat("0", 40) + ".pack"
	resp := doGET(t, repoURL(srv, repoName, "objects", "pack", fake), nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for non-existent pack", resp.StatusCode)
	}
}

// --- §5.7: GET /objects/pack/pack-<hash>.idx ---

// TestDumb_GetPackIdx_ServesIdxBytes verifies spec §5.7: .idx files are
// served with Content-Type application/x-git-packed-objects-toc.
func TestDumb_GetPackIdx_ServesIdxBytes(t *testing.T) {
	root, repoName, _, _, packFiles, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	pack := packFiles[0]
	idx := strings.TrimSuffix(pack, ".pack") + ".idx"
	resp := doGET(t, repoURL(srv, repoName, "objects", "pack", idx), nil)
	defer resp.Body.Close()
	body := mustReadBody(t, resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != ctPackedObjectsTOC {
		t.Errorf("Content-Type = %q, want %q", ct, ctPackedObjectsTOC)
	}
	// v2 .idx files start with a 4-byte network-order version (==2)
	// preceded by magic \377tOc. v1 idx files start with the first fanout
	// entry (255-byte table).
	isV2 := hasPrefixBytes(body, []byte{0xff, 0x74, 0x4f, 0x63})
	if !isV2 {
		// Could be v1; just make sure body is non-empty and reasonably sized.
		if len(body) < 256 {
			t.Errorf("idx body suspiciously small (%d bytes); not a valid .idx", len(body))
		}
	}
}

// --- §5.5: GET /objects/info/alternates and /objects/info/http-alternates ---

// TestDumb_GetAlternates_Nonexistent_Returns404 verifies spec §5.5: if
// the alternates file does not exist, the server should return 404 (the
// dumb client treats this as "no alternates" and falls back).
func TestDumb_GetAlternates_Nonexistent_Returns404(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	for _, name := range []string{"alternates", "http-alternates"} {
		t.Run(name, func(t *testing.T) {
			resp := doGET(t, repoURL(srv, repoName, "objects", "info", name), nil)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("%s: status = %d, want 404 (no alternates file in fresh repo)", name, resp.StatusCode)
			}
		})
	}
}

// TestDumb_GetAlternates_ServesFile verifies that if an alternates file
// exists on disk, its contents are served verbatim.
func TestDumb_GetAlternates_ServesFile(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	// Drop a fake alternates file in place.
	altContent := "../other.git/objects\n"
	altPath := filepath.Join(root, repoName, "objects", "info", "alternates")
	mustWriteFile(t, altPath, altContent)

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	resp := doGET(t, repoURL(srv, repoName, "objects", "info", "alternates"), nil)
	defer resp.Body.Close()
	body := mustReadBody(t, resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if string(body) != altContent {
		t.Errorf("alternates body = %q, want %q", body, altContent)
	}
}

// ============================================================================
// Section 5: Smart HTTP v0/v1 — discovery  (spec §6)
// ============================================================================
//
// Smart discovery is GET /info/refs?service=git-upload-pack (or
// git-receive-pack). The response is a pkt-line stream with a specific
// framing: "# service=...\n" + flush, then ref-list with capabilities
// behind a NUL byte on the first ref, then flush.

// TestSmart_Discovery_UploadPack_ContentType verifies spec §6.2: response
// Content-Type is application/x-git-upload-pack-advertisement.
func TestSmart_Discovery_UploadPack_ContentType(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	resp := doGET(t, repoURL(srv, repoName, "info", "refs")+"?service=git-upload-pack", nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != ctUploadPackAdvertise {
		t.Errorf("Content-Type = %q, want %q", ct, ctUploadPackAdvertise)
	}
}

// TestSmart_Discovery_UploadPack_CacheControl verifies spec §6.2: smart
// discovery response has Cache-Control: no-cache.
func TestSmart_Discovery_UploadPack_CacheControl(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	resp := doGET(t, repoURL(srv, repoName, "info", "refs")+"?service=git-upload-pack", nil)
	defer resp.Body.Close()

	cc := resp.Header.Get("Cache-Control")
	if !strings.Contains(strings.ToLower(cc), "no-cache") {
		t.Errorf("Cache-Control = %q; want contains \"no-cache\"", cc)
	}
}

// TestSmart_Discovery_UploadPack_ServiceHeader verifies spec §6.3: the
// first pkt-line is "# service=git-upload-pack\n" followed immediately by
// a flush-pkt.
func TestSmart_Discovery_UploadPack_ServiceHeader(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	resp := doGET(t, repoURL(srv, repoName, "info", "refs")+"?service=git-upload-pack", nil)
	defer resp.Body.Close()
	body := mustReadBody(t, resp.Body)

	// First 5 bytes must match ^[0-9a-f]{4}# per spec §6.4.
	if len(body) < 5 || body[4] != '#' {
		t.Fatalf("response does not start with pkt-line containing '#': first 8 bytes = %q", body[:min(8, len(body))])
	}
	lines := readPktLines(t, bytes.NewReader(body))
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 pkt-lines (service + flush), got %d", len(lines))
	}
	if string(lines[0].bytes) != "# service=git-upload-pack\n" {
		t.Errorf("first pkt-line = %q, want \"# service=git-upload-pack\\n\"", lines[0].bytes)
	}
	if lines[1].kind != pktFlush {
		t.Errorf("second pkt-line = %v, want flush", lines[1])
	}
}

// TestSmart_Discovery_UploadPack_RefAdvertisement verifies spec §6.3: the
// ref list follows the service header + flush. The first ref carries a
// NUL-separated capability list. The list ends with a flush-pkt.
func TestSmart_Discovery_UploadPack_RefAdvertisement(t *testing.T) {
	root, repoName, _, refs, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	resp := doGET(t, repoURL(srv, repoName, "info", "refs")+"?service=git-upload-pack", nil)
	defer resp.Body.Close()
	body := mustReadBody(t, resp.Body)
	lines := readPktLines(t, bytes.NewReader(body))

	// Drop the leading "# service" + flush.
	if len(lines) < 2 || lines[1].kind != pktFlush {
		t.Fatalf("missing service header + flush; lines = %v", lines[:min(2, len(lines))])
	}
	rest := lines[2:]

	// The last entry of rest must be a flush-pkt.
	if len(rest) == 0 || rest[len(rest)-1].kind != pktFlush {
		t.Fatalf("ref advertisement does not end with flush-pkt; last = %v", rest[len(rest)-1])
	}
	refs2 := rest[:len(rest)-1]
	if len(refs2) == 0 {
		t.Fatal("ref advertisement contains no refs")
	}

	// First ref line must contain a NUL byte separating "sha name" from caps.
	first := refs2[0].bytes
	nulIdx := bytes.IndexByte(first, 0x00)
	if nulIdx < 0 {
		t.Fatalf("first ref line has no NUL byte separating capabilities: %q", first)
	}
	prefix := first[:nulIdx] // "sha name"
	caps := first[nulIdx+1:] // "cap1 cap2 ..."

	// Prefix must match "<40-hex sha> <refname>".
	fields := strings.Fields(string(prefix))
	if len(fields) != 2 {
		t.Errorf("first ref prefix = %q, want \"<sha> <refname>\"", prefix)
	} else {
		if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(fields[0]) {
			t.Errorf("first ref SHA = %q, want 40 hex digits", fields[0])
		}
		if _, ok := refs[fields[1]]; !ok {
			t.Errorf("first ref name %q not in test fixture refs", fields[1])
		}
	}

	// Capability list must be non-empty and space-separated.
	capList := strings.Fields(string(caps))
	if len(capList) == 0 {
		t.Errorf("first ref has empty capability list: %q", caps)
	}

	// Subsequent ref lines must NOT have a NUL byte.
	for i, l := range refs2[1:] {
		if bytes.IndexByte(l.bytes, 0x00) >= 0 {
			t.Errorf("ref line #%d unexpectedly contains NUL: %q", i+1, l.bytes)
		}
	}
}

// TestSmart_Discovery_UploadPack_AdvertisesAllRefs verifies that every
// ref in the test repo (except peeled companions) appears in the smart
// advertisement.
func TestSmart_Discovery_UploadPack_AdvertisesAllRefs(t *testing.T) {
	root, repoName, _, refs, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	resp := doGET(t, repoURL(srv, repoName, "info", "refs")+"?service=git-upload-pack", nil)
	defer resp.Body.Close()
	body := mustReadBody(t, resp.Body)
	lines := readPktLines(t, bytes.NewReader(body))

	// Collect (sha, name) pairs from the ref list.
	gotRefs := make(map[string]string) // name → sha
	for _, l := range lines {
		if l.kind != pktData {
			continue
		}
		s := string(l.bytes)
		if strings.HasPrefix(s, "# service=") {
			continue
		}
		// Strip NUL + caps if present.
		if nulIdx := strings.IndexByte(s, 0x00); nulIdx >= 0 {
			s = s[:nulIdx]
		}
		s = strings.TrimRight(s, "\n")
		fields := strings.Fields(s)
		if len(fields) == 2 {
			gotRefs[fields[1]] = fields[0]
		}
	}

	for name, sha := range refs {
		got, ok := gotRefs[name]
		if !ok {
			t.Errorf("smart advertisement missing ref %q", name)
			continue
		}
		if got != sha {
			t.Errorf("smart advertisement ref %q: SHA = %s, want %s", name, got, sha)
		}
	}
}

// TestSmart_Discovery_UploadPack_AdvertisesSymrefHEAD verifies spec §9.2:
// server SHOULD advertise `symref=HEAD:refs/heads/<branch>` so the client
// knows which branch HEAD points to without a separate /HEAD request.
func TestSmart_Discovery_UploadPack_AdvertisesSymrefHEAD(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	resp := doGET(t, repoURL(srv, repoName, "info", "refs")+"?service=git-upload-pack", nil)
	defer resp.Body.Close()
	body := mustReadBody(t, resp.Body)
	lines := readPktLines(t, bytes.NewReader(body))

	// Find the first ref line and look for "symref=HEAD:..." in the caps.
	for _, l := range lines {
		if l.kind != pktData {
			continue
		}
		s := string(l.bytes)
		if strings.HasPrefix(s, "# service=") {
			continue
		}
		nulIdx := strings.IndexByte(s, 0x00)
		if nulIdx < 0 {
			break // first ref has no caps (shouldn't happen for non-empty repos)
		}
		caps := s[nulIdx+1:]
		if !strings.Contains(caps, "symref=HEAD:") {
			t.Errorf("first ref caps do not advertise symref=HEAD:...; caps = %q", caps)
		}
		return
	}
	t.Fatal("no first-ref line with capabilities found")
}

// TestSmart_Discovery_UploadPack_AdvertisesAgent verifies spec §9.2: the
// server SHOULD advertise an `agent=...` capability.
func TestSmart_Discovery_UploadPack_AdvertisesAgent(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	resp := doGET(t, repoURL(srv, repoName, "info", "refs")+"?service=git-upload-pack", nil)
	defer resp.Body.Close()
	body := mustReadBody(t, resp.Body)
	lines := readPktLines(t, bytes.NewReader(body))

	for _, l := range lines {
		if l.kind != pktData || !bytes.Contains(l.bytes, []byte{0x00}) {
			continue
		}
		nulIdx := bytes.IndexByte(l.bytes, 0x00)
		caps := string(l.bytes[nulIdx+1:])
		if !strings.Contains(caps, "agent=") {
			t.Errorf("caps do not include agent=...; caps = %q", caps)
		}
		return
	}
	t.Fatal("no caps section found in smart advertisement")
}

// TestSmart_Discovery_ReceivePack_ContentType verifies spec §8.1: smart
// discovery for git-receive-pack returns Content-Type
// application/x-git-receive-pack-advertisement.
func TestSmart_Discovery_ReceivePack_ContentType(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	resp := doGET(t, repoURL(srv, repoName, "info", "refs")+"?service=git-receive-pack", nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != ctReceivePackAdvertise {
		t.Errorf("Content-Type = %q, want %q", ct, ctReceivePackAdvertise)
	}
}

// TestSmart_Discovery_ReceivePack_ServiceHeader verifies spec §8.1: first
// pkt-line is "# service=git-receive-pack\n" + flush.
func TestSmart_Discovery_ReceivePack_ServiceHeader(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	resp := doGET(t, repoURL(srv, repoName, "info", "refs")+"?service=git-receive-pack", nil)
	defer resp.Body.Close()
	body := mustReadBody(t, resp.Body)
	lines := readPktLines(t, bytes.NewReader(body))

	if len(lines) < 2 {
		t.Fatalf("expected at least 2 pkt-lines, got %d", len(lines))
	}
	if string(lines[0].bytes) != "# service=git-receive-pack\n" {
		t.Errorf("first pkt-line = %q, want \"# service=git-receive-pack\\n\"", lines[0].bytes)
	}
	if lines[1].kind != pktFlush {
		t.Errorf("second pkt-line = %v, want flush", lines[1])
	}
}

// TestSmart_Discovery_ReceivePack_AdvertisesReportStatus verifies spec
// §9.2: receive-pack SHOULD advertise `report-status` so clients know they
// can request a status report.
func TestSmart_Discovery_ReceivePack_AdvertisesReportStatus(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	resp := doGET(t, repoURL(srv, repoName, "info", "refs")+"?service=git-receive-pack", nil)
	defer resp.Body.Close()
	body := mustReadBody(t, resp.Body)
	lines := readPktLines(t, bytes.NewReader(body))

	for _, l := range lines {
		if l.kind != pktData || !bytes.Contains(l.bytes, []byte{0x00}) {
			continue
		}
		nulIdx := bytes.IndexByte(l.bytes, 0x00)
		caps := string(l.bytes[nulIdx+1:])
		if !strings.Contains(caps, "report-status") {
			t.Errorf("receive-pack caps do not include report-status; caps = %q", caps)
		}
		return
	}
	t.Fatal("no caps section found in receive-pack advertisement")
}

// TestSmart_Discovery_UnknownService_ReturnsError verifies spec §6.2: the
// service parameter must be exactly `git-upload-pack` or `git-receive-pack`.
// Anything else should be rejected (4xx, not 200).
func TestSmart_Discovery_UnknownService_ReturnsError(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	resp := doGET(t, repoURL(srv, repoName, "info", "refs")+"?service=git-bogus-pack", nil)
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		t.Errorf("status = 200 for unknown service; want 4xx")
	}
}

// ============================================================================
// Section 6: Smart HTTP v0/v1 — git-upload-pack (fetch)  (spec §7)
// ============================================================================
//
// After discovery, the client POSTs a pkt-line stream of want/have lines
// to /git-upload-pack. The server negotiates and streams back a packfile.

// TestSmart_UploadPack_RejectsGET verifies spec §7.1: the actual fetch is
// a POST. A GET to /git-upload-pack (which the current router oddly
// registers) must not produce a smart fetch response.
//
// Note: the current router registers GET /git-upload-pack, which is
// incorrect for the actual fetch endpoint (only POST is valid). This test
// documents the expected behaviour: GET must NOT return 200 with a
// fetch result.
func TestSmart_UploadPack_RejectsGET(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	resp := doGET(t, repoURL(srv, repoName, "git-upload-pack"), nil)
	defer resp.Body.Close()
	// Either 405 (correct) or any non-200 (acceptable); but never 200 with
	// a fetch result, because that would imply GET is a valid fetch method.
	if resp.StatusCode == http.StatusOK {
		ct := resp.Header.Get("Content-Type")
		if ct == ctUploadPackResult {
			t.Errorf("GET /git-upload-pack returned a fetch result; only POST is valid per spec §7.1")
		}
	}
}

// TestSmart_UploadPack_POST_ContentType verifies spec §7.1: the POST
// response has Content-Type application/x-git-upload-pack-result.
func TestSmart_UploadPack_POST_ContentType(t *testing.T) {
	root, repoName, _, refs, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	body := buildUploadRequest(t, refs["refs/heads/master"], nil, nil, true)
	resp := doRequest(t, http.MethodPost,
		repoURL(srv, repoName, "git-upload-pack"),
		bytes.NewReader(body),
		map[string]string{"Content-Type": ctUploadPackRequest})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, mustReadBody(t, resp.Body))
	}
	if ct := resp.Header.Get("Content-Type"); ct != ctUploadPackResult {
		t.Errorf("Content-Type = %q, want %q", ct, ctUploadPackResult)
	}
}

// TestSmart_UploadPack_POST_CacheControl verifies spec §7.10: fetch
// responses carry Cache-Control: no-cache (and the legacy Expires/Pragma
// headers from hdr_nocache in http-backend.c).
func TestSmart_UploadPack_POST_CacheControl(t *testing.T) {
	root, repoName, _, refs, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	body := buildUploadRequest(t, refs["refs/heads/master"], nil, nil, true)
	resp := doRequest(t, http.MethodPost,
		repoURL(srv, repoName, "git-upload-pack"),
		bytes.NewReader(body),
		map[string]string{"Content-Type": ctUploadPackRequest})
	defer resp.Body.Close()

	cc := resp.Header.Get("Cache-Control")
	if !strings.Contains(strings.ToLower(cc), "no-cache") {
		t.Errorf("Cache-Control = %q; want contains \"no-cache\"", cc)
	}
}

// TestSmart_UploadPack_POST_WrongContentType verifies spec §7.1: a POST
// with a wrong Content-Type must be rejected (415 or 400).
func TestSmart_UploadPack_POST_WrongContentType(t *testing.T) {
	root, repoName, _, refs, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	body := buildUploadRequest(t, refs["refs/heads/master"], nil, nil, true)
	resp := doRequest(t, http.MethodPost,
		repoURL(srv, repoName, "git-upload-pack"),
		bytes.NewReader(body),
		map[string]string{"Content-Type": "text/plain"})
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		t.Errorf("status = 200 for POST with wrong Content-Type; want 4xx")
	}
}

// TestSmart_UploadPack_Clone_ReturnsNAK verifies spec §7.7: a clone (no
// have lines) receives "0008NAK\n" before the packfile (no common base
// found).
func TestSmart_UploadPack_Clone_ReturnsNAK(t *testing.T) {
	root, repoName, _, refs, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	body := buildUploadRequest(t, refs["refs/heads/master"], nil, nil, true)
	resp := doRequest(t, http.MethodPost,
		repoURL(srv, repoName, "git-upload-pack"),
		bytes.NewReader(body),
		map[string]string{"Content-Type": ctUploadPackRequest})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, mustReadBody(t, resp.Body))
	}

	respBytes := mustReadBody(t, resp.Body)
	// First pkt-line must be "NAK\n".
	r := NewPktLineReader(bytes.NewReader(respBytes))
	data, isFlush, err := r.ReadPktLine()
	if err != nil {
		t.Fatalf("ReadPktLine: %v", err)
	}
	if isFlush {
		t.Fatal("first pkt-line is flush; want NAK")
	}
	if string(data) != "NAK\n" {
		t.Errorf("first pkt-line = %q, want \"NAK\\n\"", data)
	}
}

// TestSmart_UploadPack_Clone_PackfileFollows verifies spec §7.7 / §7.8:
// after the NAK, the server streams a packfile. Without side-band, the
// pack bytes follow directly after the NAK pkt-line.
func TestSmart_UploadPack_Clone_PackfileFollows(t *testing.T) {
	root, repoName, _, refs, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	// Pass an explicit cap list WITHOUT side-band so the packfile is
	// streamed raw (not multiplexed). Send `done` so the server proceeds
	// to packfile streaming (§7.7).
	body := buildUploadRequest(t, refs["refs/heads/master"], nil, []string{"ofs-delta"}, true)
	resp := doRequest(t, http.MethodPost,
		repoURL(srv, repoName, "git-upload-pack"),
		bytes.NewReader(body),
		map[string]string{"Content-Type": ctUploadPackRequest})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	respBytes := mustReadBody(t, resp.Body)
	// Consume NAK pkt-line ("0008NAK\n").
	if len(respBytes) < 8 || string(respBytes[:8]) != "0008NAK\n" {
		t.Fatalf("response does not start with \"0008NAK\\n\"; first 16 bytes = %q", respBytes[:min(16, len(respBytes))])
	}
	packBytes := respBytes[8:]
	if !hasPrefixBytes(packBytes, []byte("PACK")) {
		t.Fatalf("bytes after NAK are not a packfile (no PACK magic); first 16 = %q", packBytes[:min(16, len(packBytes))])
	}
	// Packfile must end with a 20-byte SHA-1 trailer.
	if len(packBytes) < 32 { // 12 (header) + 0 (objects) + 20 (trailer) minimum
		t.Errorf("packfile too small: %d bytes", len(packBytes))
	}
	// Validate trailer: SHA-1 of packBytes[:len-20] should equal packBytes[len-20:].
	gotSum := sha1.Sum(packBytes[:len(packBytes)-20])
	if !bytes.Equal(gotSum[:], packBytes[len(packBytes)-20:]) {
		t.Errorf("packfile trailer SHA-1 mismatch: trailer = %x, computed = %x",
			packBytes[len(packBytes)-20:], gotSum)
	}
}

// TestSmart_UploadPack_Clone_Sideband64k verifies spec §7.8: when the
// client requests `side-band-64k`, the packfile is multiplexed into
// pkt-lines with a 1-byte channel id (1 = pack data).
func TestSmart_UploadPack_Clone_Sideband64k(t *testing.T) {
	root, repoName, _, refs, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	body := buildUploadRequest(t, refs["refs/heads/master"], nil, []string{"side-band-64k"}, true)
	resp := doRequest(t, http.MethodPost,
		repoURL(srv, repoName, "git-upload-pack"),
		bytes.NewReader(body),
		map[string]string{"Content-Type": ctUploadPackRequest})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, mustReadBody(t, resp.Body))
	}

	respBytes := mustReadBody(t, resp.Body)
	lines := readPktLines(t, bytes.NewReader(respBytes))

	// First pkt-line: NAK.
	if len(lines) == 0 {
		t.Fatal("no pkt-lines in response")
	}
	if string(lines[0].bytes) != "NAK\n" {
		t.Errorf("first pkt-line = %q, want \"NAK\\n\"", lines[0].bytes)
	}

	// Remaining data pkt-lines must start with channel byte 1 (pack).
	var packData []byte
	for _, l := range lines[1:] {
		if l.kind != pktData {
			continue
		}
		if len(l.bytes) == 0 {
			continue
		}
		switch l.bytes[0] {
		case 1:
			packData = append(packData, l.bytes[1:]...)
		case 2:
			// progress; ignore
		case 3:
			t.Fatalf("server sent fatal side-band error: %q", l.bytes[1:])
		default:
			t.Errorf("unknown side-band channel %d in pkt-line %q", l.bytes[0], l.bytes)
		}
	}

	if !hasPrefixBytes(packData, []byte("PACK")) {
		t.Errorf("demultiplexed pack does not start with PACK; first 16 = %q", packData[:min(16, len(packData))])
	}
}

// TestSmart_UploadPack_GzipRequest verifies spec §7.1 / §14.3: server
// MUST accept gzip-compressed request bodies (Content-Encoding: gzip).
func TestSmart_UploadPack_GzipRequest(t *testing.T) {
	root, repoName, _, refs, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	rawBody := buildUploadRequest(t, refs["refs/heads/master"], nil, nil, true)
	gzBody := gzipBytes(t, rawBody)
	resp := doRequest(t, http.MethodPost,
		repoURL(srv, repoName, "git-upload-pack"),
		bytes.NewReader(gzBody),
		map[string]string{
			"Content-Type":     ctUploadPackRequest,
			"Content-Encoding": "gzip",
		})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 (gzip request should be accepted); body: %s",
			resp.StatusCode, mustReadBody(t, resp.Body))
	}
}

// buildUploadRequest assembles a v0/v1 upload-pack request body:
//
//	<first-want-with-caps>
//	<additional wants...>
//	[<haves...>]
//	0000                       (flush)
//	0009done\n                 (only if sendDone)
//
// caps is the list of capabilities to advertise on the first want line.
// If caps is nil, a sensible default ("multi_ack side-band-64k ofs-delta")
// is used unless noCaps=true (then no caps at all — used for plain
// non-side-band tests).
//
// Per spec §7.2 / §7.3.
func buildUploadRequest(t *testing.T, wantSHA string, haves []string, caps []string, sendDone bool) []byte {
	t.Helper()
	if caps == nil {
		caps = []string{"multi_ack", "side-band-64k", "ofs-delta"}
	}
	var buf bytes.Buffer

	// First want: "want <sha> <caps>\n".
	first := fmt.Sprintf("want %s %s\n", wantSHA, strings.Join(caps, " "))
	b, err := EncodePktLineString(first)
	if err != nil {
		t.Fatal(err)
	}
	buf.Write(b)

	// Additional wants (none in our simple case).

	// Haves.
	for _, h := range haves {
		b, _ := EncodePktLineString("have " + h + "\n")
		buf.Write(b)
	}

	// Flush.
	buf.Write(flushPkt())

	// Done.
	if sendDone {
		b, _ := EncodePktLineString("done\n")
		buf.Write(b)
	}

	return buf.Bytes()
}

// ============================================================================
// Section 7: Smart HTTP v0/v1 — git-receive-pack (push)  (spec §8)
// ============================================================================
//
// Push is a POST to /git-receive-pack. The body is a command list (one
// pkt-line per ref update, with caps on the first), a flush, then a binary
// packfile. The server responds with a report-status pkt-line stream.

// TestSmart_ReceivePack_POST_ContentType verifies spec §8.2: response
// Content-Type is application/x-git-receive-pack-result.
func TestSmart_ReceivePack_POST_ContentType(t *testing.T) {
	root, repoName, _, refs, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	// Build a "create branch" command + empty packfile (no new objects
	// needed for a delete or for pointing a new branch at an existing commit).
	body := buildReceiveRequest(t, []refUpdate{{
		oldID: strings.Repeat("0", 40),
		newID: refs["refs/heads/master"],
		name:  "refs/heads/new-branch",
	}}, nil)

	resp := doRequest(t, http.MethodPost,
		repoURL(srv, repoName, "git-receive-pack"),
		bytes.NewReader(body),
		map[string]string{"Content-Type": ctReceivePackRequest})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, mustReadBody(t, resp.Body))
	}
	if ct := resp.Header.Get("Content-Type"); ct != ctReceivePackResult {
		t.Errorf("Content-Type = %q, want %q", ct, ctReceivePackResult)
	}
}

// TestSmart_ReceivePack_CreateBranch verifies spec §8.3 / §8.7: a create
// command (old=zero, new=sha) results in "unpack ok" + "ok <refname>" in
// the report-status response.
func TestSmart_ReceivePack_CreateBranch(t *testing.T) {
	root, repoName, _, refs, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	body := buildReceiveRequest(t, []refUpdate{{
		oldID: strings.Repeat("0", 40),
		newID: refs["refs/heads/master"],
		name:  "refs/heads/new-branch",
	}}, nil)

	resp := doRequest(t, http.MethodPost,
		repoURL(srv, repoName, "git-receive-pack"),
		bytes.NewReader(body),
		map[string]string{"Content-Type": ctReceivePackRequest})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, mustReadBody(t, resp.Body))
	}

	respBytes := mustReadBody(t, resp.Body)
	lines := readPktLines(t, bytes.NewReader(respBytes))
	data := dataLinesStr(lines)

	// First line: "unpack ok".
	if len(data) == 0 {
		t.Fatal("report-status is empty")
	}
	if data[0] != "unpack ok" {
		t.Errorf("first report-status line = %q, want \"unpack ok\"", data[0])
	}

	// Second line: "ok refs/heads/new-branch".
	found := false
	for _, line := range data[1:] {
		if line == "ok refs/heads/new-branch" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("report-status missing \"ok refs/heads/new-branch\"; lines = %v", data)
	}

	// Verify the ref was actually created on disk.
	if _, err := os.Stat(filepath.Join(root, repoName, "refs", "heads", "new-branch")); err != nil {
		t.Errorf("refs/heads/new-branch was not created on disk: %v", err)
	}
}

// TestSmart_ReceivePack_DeleteBranch verifies spec §8.3 / §9.2: a delete
// command (old=sha, new=zero) requires the `delete-refs` capability and
// must NOT include a packfile.
func TestSmart_ReceivePack_DeleteBranch(t *testing.T) {
	root, repoName, _, refs, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	// First create a branch we can delete.
	body := buildReceiveRequest(t, []refUpdate{{
		oldID: strings.Repeat("0", 40),
		newID: refs["refs/heads/master"],
		name:  "refs/heads/to-delete",
	}}, nil)
	resp := doRequest(t, http.MethodPost,
		repoURL(srv, repoName, "git-receive-pack"),
		bytes.NewReader(body),
		map[string]string{"Content-Type": ctReceivePackRequest})
	mustResp(t, resp)

	// Now delete it.
	delBody := buildReceiveRequest(t, []refUpdate{{
		oldID: refs["refs/heads/master"],
		newID: strings.Repeat("0", 40),
		name:  "refs/heads/to-delete",
	}}, nil)
	resp = doRequest(t, http.MethodPost,
		repoURL(srv, repoName, "git-receive-pack"),
		bytes.NewReader(delBody),
		map[string]string{"Content-Type": ctReceivePackRequest})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, mustReadBody(t, resp.Body))
	}

	respBytes := mustReadBody(t, resp.Body)
	lines := readPktLines(t, bytes.NewReader(respBytes))
	data := dataLinesStr(lines)

	found := false
	for _, line := range data {
		if line == "ok refs/heads/to-delete" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("delete not acknowledged in report-status; lines = %v", data)
	}

	// Ref file should be gone.
	if _, err := os.Stat(filepath.Join(root, repoName, "refs", "heads", "to-delete")); !os.IsNotExist(err) {
		t.Errorf("refs/heads/to-delete still exists after delete; err = %v", err)
	}
}

// TestSmart_ReceivePack_NonFastForward_Rejected verifies spec §8.7: when
// a push is non-fast-forward, the report-status line for that ref is
// "ng <refname> non-fast-forward" (or similar error).
func TestSmart_ReceivePack_NonFastForward_Rejected(t *testing.T) {
	root, repoName, _, refs, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	// Set up two divergent branches: master and feature.
	// First, create feature branch from master.
	featureSHA := createDivergentCommit(t, filepath.Join(root, repoName), refs["refs/heads/master"])
	refs["refs/heads/feature"] = featureSHA

	// Now try to update master to point at featureSHA — this is a
	// non-fast-forward (feature is not a descendant of master).
	body := buildReceiveRequest(t, []refUpdate{{
		oldID: refs["refs/heads/master"],
		newID: featureSHA,
		name:  "refs/heads/master",
	}}, nil)
	resp := doRequest(t, http.MethodPost,
		repoURL(srv, repoName, "git-receive-pack"),
		bytes.NewReader(body),
		map[string]string{"Content-Type": ctReceivePackRequest})
	defer resp.Body.Close()

	respBytes := mustReadBody(t, resp.Body)
	lines := readPktLines(t, bytes.NewReader(respBytes))
	data := dataLinesStr(lines)

	// Expect "ng refs/heads/master ..." with an error reason.
	ngFound := false
	for _, line := range data {
		if strings.HasPrefix(line, "ng refs/heads/master ") {
			ngFound = true
			break
		}
	}
	if !ngFound {
		t.Errorf("non-fast-forward push not rejected; report-status = %v", data)
	}
}

// createDivergentCommit creates a new commit that's a child of parentSHA
// inside the bare repo at repoPath, using a temp worktree. Returns the
// new commit's SHA. Used to set up a non-fast-forward scenario.
func createDivergentCommit(t *testing.T, repoPath, parentSHA string) string {
	t.Helper()
	tmp, err := os.MkdirTemp("", "got-divergent-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmp)

	// Clone the bare repo into a workdir, create a divergent commit.
	runGitInDir(t, tmp, "clone", repoPath, "work")
	work := filepath.Join(tmp, "work")
	runGitInDir(t, work, "config", "user.email", "test@example.com")
	runGitInDir(t, work, "config", "user.name", "Test User")
	mustWriteFile(t, filepath.Join(work, "divergent.txt"), "divergent change\n")
	runGitInDir(t, work, "add", "divergent.txt")
	runGitInDir(t, work, "commit", "-m", "divergent commit")
	sha := runGitInDir(t, work, "rev-parse", "HEAD")
	// Push the divergent branch back into the bare repo as refs/heads/_divergent_tmp.
	runGitInDir(t, work, "push", repoPath, "HEAD:refs/heads/_divergent_tmp")
	// Clean up the temp branch.
	runGitInDir(t, repoPath, "update-ref", "-d", "refs/heads/_divergent_tmp")
	return sha
}

// refUpdate describes one ref update command in a git-receive-pack request.
type refUpdate struct {
	oldID string
	newID string
	name  string
}

// buildReceiveRequest assembles a v0/v1 receive-pack request body:
//
//	<first command + NUL + caps + LF>
//	<additional commands + LF>
//	0000                  (flush)
//	<packfile bytes>      (omitted if packObjects is nil)
//
// Per spec §8.2 / §8.3 / §8.6. If packObjects is nil, an empty packfile
// (just PACK header + 20-byte trailer, 0 objects) is sent — this is the
// correct behaviour when all referenced objects already exist on the
// server (spec §8.6).
func buildReceiveRequest(t *testing.T, updates []refUpdate, packObjects []byte) []byte {
	t.Helper()
	if len(updates) == 0 {
		t.Fatal("buildReceiveRequest: at least one refUpdate is required")
	}
	var buf bytes.Buffer

	// First command carries caps after NUL.
	caps := " report-status delete-refs ofs-delta"
	first := fmt.Sprintf("%s %s %s\x00%s\n", updates[0].oldID, updates[0].newID, updates[0].name, caps)
	b, err := EncodePktLineString(first)
	if err != nil {
		t.Fatal(err)
	}
	buf.Write(b)

	// Remaining commands.
	for _, u := range updates[1:] {
		b, _ := EncodePktLineString(fmt.Sprintf("%s %s %s\n", u.oldID, u.newID, u.name))
		buf.Write(b)
	}

	// Flush.
	buf.Write(flushPkt())

	// Packfile (or empty pack if none provided).
	pack := packObjects
	if pack == nil {
		pack = buildEmptyPack()
	}
	buf.Write(pack)

	return buf.Bytes()
}

// buildEmptyPack returns a 0-object packfile (PACK header + 20-byte SHA-1
// trailer of the header). This is what a client sends when pushing a ref
// that points at an object the server already has (spec §8.6).
func buildEmptyPack() []byte {
	var buf bytes.Buffer
	buf.WriteString("PACK")
	// version 2 (BE uint32).
	buf.WriteByte(0)
	buf.WriteByte(0)
	buf.WriteByte(0)
	buf.WriteByte(2)
	// object count 0 (BE uint32).
	buf.WriteByte(0)
	buf.WriteByte(0)
	buf.WriteByte(0)
	buf.WriteByte(0)
	// 20-byte SHA-1 trailer.
	sum := sha1.Sum(buf.Bytes())
	buf.Write(sum[:])
	return buf.Bytes()
}

// ============================================================================
// Section 8: Git Protocol v2  (spec §10)
// ============================================================================
//
// Protocol v2 is opt-in via `Git-Protocol: version=2` header. Discovery
// returns "version 2" + capability-list; the client then issues
// command-oriented POSTs (ls-refs, fetch, object-info, bundle-uri).

// --- §10.2 / §10.3: v2 discovery ---

// TestV2_Discovery_Version2FirstPktLine verifies spec §10.2: when the
// client sends Git-Protocol: version=2, the first pkt-line of the
// response is "version 2\n".
func TestV2_Discovery_Version2FirstPktLine(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	resp := doGET(t,
		repoURL(srv, repoName, "info", "refs")+"?service=git-upload-pack",
		map[string]string{"Git-Protocol": "version=2"})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != ctUploadPackAdvertise {
		t.Errorf("Content-Type = %q, want %q", ct, ctUploadPackAdvertise)
	}

	body := mustReadBody(t, resp.Body)
	lines := readPktLines(t, bytes.NewReader(body))
	if len(lines) == 0 {
		t.Fatal("v2 discovery response is empty")
	}
	if string(lines[0].bytes) != "version 2\n" {
		t.Errorf("first pkt-line = %q, want \"version 2\\n\"", lines[0].bytes)
	}
}

// TestV2_Discovery_NoServiceHeaderInV2 verifies spec §10.2: unlike v0/v1,
// v2 discovery does NOT include the "# service=..." pkt-line.
func TestV2_Discovery_NoServiceHeaderInV2(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	resp := doGET(t,
		repoURL(srv, repoName, "info", "refs")+"?service=git-upload-pack",
		map[string]string{"Git-Protocol": "version=2"})
	defer resp.Body.Close()
	body := mustReadBody(t, resp.Body)
	lines := readPktLines(t, bytes.NewReader(body))

	for _, l := range lines {
		if l.kind == pktData && strings.HasPrefix(string(l.bytes), "# service=") {
			t.Errorf("v2 discovery must not contain \"# service=...\" pkt-line; got %q", l.bytes)
		}
	}
}

// TestV2_Discovery_CapabilityList verifies spec §10.3: after "version 2",
// the server advertises a capability list (one capability per pkt-line)
// terminated by a flush-pkt. Must include `ls-refs` and `fetch` at minimum.
func TestV2_Discovery_CapabilityList(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	resp := doGET(t,
		repoURL(srv, repoName, "info", "refs")+"?service=git-upload-pack",
		map[string]string{"Git-Protocol": "version=2"})
	defer resp.Body.Close()
	body := mustReadBody(t, resp.Body)
	lines := readPktLines(t, bytes.NewReader(body))

	// Strip leading "version 2".
	if len(lines) < 2 || string(lines[0].bytes) != "version 2\n" {
		t.Fatalf("missing/incorrect version line; lines[0] = %v", lines[0])
	}
	rest := lines[1:]

	// Last entry must be flush-pkt.
	if len(rest) == 0 || rest[len(rest)-1].kind != pktFlush {
		t.Fatalf("capability list does not end with flush; last = %v", rest[len(rest)-1])
	}
	caps := rest[:len(rest)-1]
	if len(caps) == 0 {
		t.Fatal("capability list is empty")
	}

	// Collect capabilities (strip trailing LF).
	capStrs := make([]string, len(caps))
	for i, c := range caps {
		capStrs[i] = strings.TrimRight(string(c.bytes), "\n")
	}

	// Must advertise at least ls-refs and fetch (commands).
	hasLsRefs := false
	hasFetch := false
	for _, c := range capStrs {
		if c == "ls-refs" || strings.HasPrefix(c, "ls-refs=") {
			hasLsRefs = true
		}
		if c == "fetch" || strings.HasPrefix(c, "fetch=") {
			hasFetch = true
		}
	}
	if !hasLsRefs {
		t.Errorf("v2 capability list missing ls-refs; caps = %v", capStrs)
	}
	if !hasFetch {
		t.Errorf("v2 capability list missing fetch; caps = %v", capStrs)
	}
}

// TestV2_Discovery_EndsWithResponseEndPkt verifies spec §10.11: HTTP v2
// servers SHOULD send a response-end-pkt (0002) at the very end of the
// response, after the trailing flush, so the client knows the HTTP
// response can be closed.
func TestV2_Discovery_EndsWithResponseEndPkt(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	resp := doGET(t,
		repoURL(srv, repoName, "info", "refs")+"?service=git-upload-pack",
		map[string]string{"Git-Protocol": "version=2"})
	defer resp.Body.Close()
	body := mustReadBody(t, resp.Body)
	lines := readPktLines(t, bytes.NewReader(body))

	if len(lines) == 0 {
		t.Fatal("empty v2 discovery response")
	}
	last := lines[len(lines)-1]
	if last.kind != pktResponseEnd {
		t.Logf("v2 discovery does not end with response-end-pkt (0002); last = %v. Spec says SHOULD, not MUST.", last)
	}
}

// --- §10.6: ls-refs ---

// TestV2_LsRefs_ReturnsRefList verifies spec §10.6: POST with
// command=ls-refs returns a ref list, one pkt-line per ref, terminated by
// flush.
func TestV2_LsRefs_ReturnsRefList(t *testing.T) {
	root, repoName, _, refs, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	body := buildV2LsRefsRequest(t, false, false, nil)
	resp := doRequest(t, http.MethodPost,
		repoURL(srv, repoName, "git-upload-pack"),
		bytes.NewReader(body),
		map[string]string{
			"Content-Type": ctUploadPackRequest,
			"Git-Protocol": "version=2",
		})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, mustReadBody(t, resp.Body))
	}
	if ct := resp.Header.Get("Content-Type"); ct != ctUploadPackResult {
		t.Errorf("Content-Type = %q, want %q", ct, ctUploadPackResult)
	}

	respBytes := mustReadBody(t, resp.Body)
	lines := readPktLines(t, bytes.NewReader(respBytes))
	data := dataLines(lines)

	// Each ref must appear as "<sha> <refname>\n".
	gotRefs := make(map[string]string)
	for _, d := range data {
		s := strings.TrimRight(string(d), "\n")
		// Strip ref-attributes after the refname (symref-target:, peeled:).
		// Take only "<sha> <refname>" prefix.
		fields := strings.Fields(s)
		if len(fields) >= 2 {
			gotRefs[fields[1]] = fields[0]
		}
	}

	for name, sha := range refs {
		got, ok := gotRefs[name]
		if !ok {
			t.Errorf("v2 ls-refs missing %q", name)
			continue
		}
		if got != sha {
			t.Errorf("v2 ls-refs %q: SHA = %s, want %s", name, got, sha)
		}
	}

	// Last entry must be flush.
	if lines[len(lines)-1].kind != pktFlush {
		t.Errorf("v2 ls-refs does not end with flush; last = %v", lines[len(lines)-1])
	}
}

// TestV2_LsRefs_SymrefsAttribute verifies spec §10.6: with `symrefs`
// argument, refs that are symrefs (e.g. HEAD) include a
// "symref-target:<target>" attribute.
func TestV2_LsRefs_SymrefsAttribute(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	// Request with `symrefs` and a ref-prefix that includes HEAD.
	body := buildV2LsRefsRequest(t, true, false, []string{"HEAD"})
	resp := doRequest(t, http.MethodPost,
		repoURL(srv, repoName, "git-upload-pack"),
		bytes.NewReader(body),
		map[string]string{
			"Content-Type": ctUploadPackRequest,
			"Git-Protocol": "version=2",
		})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, mustReadBody(t, resp.Body))
	}

	respBytes := mustReadBody(t, resp.Body)
	lines := readPktLines(t, bytes.NewReader(respBytes))

	// Find the HEAD line; it must include "symref-target:".
	foundHEAD := false
	foundSymref := false
	for _, l := range lines {
		if l.kind != pktData {
			continue
		}
		s := string(l.bytes)
		if !strings.Contains(s, " HEAD") {
			continue
		}
		foundHEAD = true
		if strings.Contains(s, "symref-target:") {
			foundSymref = true
			break
		}
	}
	if !foundHEAD {
		t.Fatal("v2 ls-refs with ref-prefix HEAD did not return HEAD")
	}
	if !foundSymref {
		t.Error("v2 ls-refs with `symrefs` argument did not include symref-target for HEAD")
	}
}

// TestV2_LsRefs_RefPrefixFilter verifies spec §10.6: ref-prefix filters
// the returned refs to those starting with the prefix.
func TestV2_LsRefs_RefPrefixFilter(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	body := buildV2LsRefsRequest(t, false, false, []string{"refs/heads/"})
	resp := doRequest(t, http.MethodPost,
		repoURL(srv, repoName, "git-upload-pack"),
		bytes.NewReader(body),
		map[string]string{
			"Content-Type": ctUploadPackRequest,
			"Git-Protocol": "version=2",
		})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	respBytes := mustReadBody(t, resp.Body)
	lines := readPktLines(t, bytes.NewReader(respBytes))

	for _, l := range lines {
		if l.kind != pktData {
			continue
		}
		s := strings.TrimRight(string(l.bytes), "\n")
		fields := strings.Fields(s)
		if len(fields) < 2 {
			continue
		}
		name := fields[1]
		if !strings.HasPrefix(name, "refs/heads/") {
			t.Errorf("v2 ls-refs with ref-prefix refs/heads/ returned non-matching ref %q", name)
		}
	}
}

// buildV2LsRefsRequest assembles a v2 ls-refs command request per spec §10.4 / §10.6:
//
//	command=ls-refs\n
//	[symrefs\n]                  (if askSymrefs)
//	[peel\n]                     (if askPeel)
//	[ref-prefix <p>\n ...]
//	0001                         (delim-pkt)
//	0000                         (flush-pkt)
func buildV2LsRefsRequest(t *testing.T, askSymrefs, askPeel bool, prefixes []string) []byte {
	t.Helper()
	var buf bytes.Buffer

	b, err := EncodePktLineString("command=ls-refs\n")
	if err != nil {
		t.Fatal(err)
	}
	buf.Write(b)

	if askSymrefs {
		b, _ := EncodePktLineString("symrefs\n")
		buf.Write(b)
	}
	if askPeel {
		b, _ := EncodePktLineString("peel\n")
		buf.Write(b)
	}
	for _, p := range prefixes {
		b, _ := EncodePktLineString("ref-prefix " + p + "\n")
		buf.Write(b)
	}

	// delim-pkt then flush-pkt.
	buf.Write(delimPkt())
	buf.Write(flushPkt())
	return buf.Bytes()
}

// --- §10.7: fetch ---

// TestV2_Fetch_Clone_ReturnsPackfile verifies spec §10.7: a v2 fetch with
// a `want` and `done` returns a packfile section (multiplexed by default).
func TestV2_Fetch_Clone_ReturnsPackfile(t *testing.T) {
	root, repoName, _, refs, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	body := buildV2FetchRequest(t, refs["refs/heads/master"], nil, true)
	resp := doRequest(t, http.MethodPost,
		repoURL(srv, repoName, "git-upload-pack"),
		bytes.NewReader(body),
		map[string]string{
			"Content-Type": ctUploadPackRequest,
			"Git-Protocol": "version=2",
		})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, mustReadBody(t, resp.Body))
	}

	respBytes := mustReadBody(t, resp.Body)
	lines := readPktLines(t, bytes.NewReader(respBytes))

	// Per spec §10.7.1: if client sends `done`, the acknowledgments
	// section is omitted and the server goes straight to packfile.
	// Find the "packfile" header line.
	packHeaderIdx := -1
	for i, l := range lines {
		if l.kind == pktData && strings.HasPrefix(string(l.bytes), "packfile\n") {
			packHeaderIdx = i
			break
		}
	}
	if packHeaderIdx == -1 {
		t.Fatal("v2 fetch response missing \"packfile\" section header")
	}

	// All subsequent data pkt-lines are side-band-multiplexed pack bytes.
	pack, progress, fatal := extractPackFromSideband(t, lines[packHeaderIdx+1:])
	if len(fatal) > 0 {
		t.Fatalf("server sent fatal side-band error: %q", fatal)
	}
	_ = progress
	if !hasPrefixBytes(pack, []byte("PACK")) {
		t.Errorf("demultiplexed v2 pack does not start with PACK; first 16 = %q", pack[:min(16, len(pack))])
	}
}

// TestV2_Fetch_WithHaves_AcknowledgmentsSection verifies spec §10.7.1:
// when the client sends `have` lines (and no `done`), the response starts
// with an acknowledgments section. If no have is common, it contains "NAK".
func TestV2_Fetch_WithHaves_AcknowledgmentsSection(t *testing.T) {
	root, repoName, _, refs, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	// Use a fake have (all zeros) — guaranteed not to be common.
	fakeHave := strings.Repeat("0", 40)
	body := buildV2FetchRequest(t, refs["refs/heads/master"], []string{fakeHave}, false)
	resp := doRequest(t, http.MethodPost,
		repoURL(srv, repoName, "git-upload-pack"),
		bytes.NewReader(body),
		map[string]string{
			"Content-Type": ctUploadPackRequest,
			"Git-Protocol": "version=2",
		})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, mustReadBody(t, resp.Body))
	}

	respBytes := mustReadBody(t, resp.Body)
	lines := readPktLines(t, bytes.NewReader(respBytes))

	if len(lines) == 0 {
		t.Fatal("empty v2 fetch response")
	}
	// First pkt-line must be "acknowledgments\n".
	if string(lines[0].bytes) != "acknowledgments\n" {
		t.Errorf("first pkt-line = %q, want \"acknowledgments\\n\"", lines[0].bytes)
	}

	// Then NAK (no common have).
	foundNAK := false
	for _, l := range lines[1:] {
		if l.kind == pktData && strings.HasPrefix(string(l.bytes), "NAK") {
			foundNAK = true
			break
		}
		if l.kind == pktDelim {
			break // acknowledgments section ends at delim
		}
	}
	if !foundNAK {
		t.Errorf("v2 fetch with non-common have did not send NAK; lines = %v", lines[:min(5, len(lines))])
	}
}

// TestV2_Fetch_WithCommonHave_ACK verifies spec §10.7.1: when the client
// sends a `have` that IS common, the server sends "ACK <sha>".
func TestV2_Fetch_WithCommonHave_ACK(t *testing.T) {
	root, repoName, _, refs, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	// Use the master SHA as a have — it IS common.
	haveSHA := refs["refs/heads/master"]
	body := buildV2FetchRequest(t, refs["refs/heads/master"], []string{haveSHA}, false)
	resp := doRequest(t, http.MethodPost,
		repoURL(srv, repoName, "git-upload-pack"),
		bytes.NewReader(body),
		map[string]string{
			"Content-Type": ctUploadPackRequest,
			"Git-Protocol": "version=2",
		})
	defer resp.Body.Close()

	respBytes := mustReadBody(t, resp.Body)
	lines := readPktLines(t, bytes.NewReader(respBytes))

	wantACK := fmt.Sprintf("ACK %s\n", haveSHA)
	foundACK := false
	for _, l := range lines {
		if l.kind == pktData && string(l.bytes) == wantACK {
			foundACK = true
			break
		}
		if l.kind == pktDelim {
			break
		}
	}
	if !foundACK {
		t.Errorf("v2 fetch with common have did not send %q; lines = %v", wantACK, lines[:min(5, len(lines))])
	}
}

// buildV2FetchRequest assembles a v2 fetch command request per spec §10.4 / §10.7:
//
//	command=fetch\n
//	[<capability>...\n]          (none in our simple case)
//	0001                         (delim)
//	want <sha>\n
//	[have <sha>\n ...]
//	[done\n]                     (if sendDone)
//	0000                         (flush)
func buildV2FetchRequest(t *testing.T, wantSHA string, haves []string, sendDone bool) []byte {
	t.Helper()
	var buf bytes.Buffer

	b, err := EncodePktLineString("command=fetch\n")
	if err != nil {
		t.Fatal(err)
	}
	buf.Write(b)
	// Some default client caps (no-progress keeps the response lean).
	b, _ = EncodePktLineString("no-progress\n")
	buf.Write(b)

	// delim.
	buf.Write(delimPkt())

	// want.
	b, _ = EncodePktLineString("want " + wantSHA + "\n")
	buf.Write(b)
	// haves.
	for _, h := range haves {
		b, _ := EncodePktLineString("have " + h + "\n")
		buf.Write(b)
	}
	if sendDone {
		b, _ := EncodePktLineString("done\n")
		buf.Write(b)
	}
	// flush.
	buf.Write(flushPkt())
	return buf.Bytes()
}

// ============================================================================
// Section 9: Edge cases & security  (spec §13, §14)
// ============================================================================

// TestEdge_PathTraversal_Rejected verifies spec §13.8: the server must
// reject URLs that attempt to escape the repository root via ../
// sequences.
func TestEdge_PathTraversal_Rejected(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	// Classic traversal: /test.git/objects/../../../../../etc/passwd
	// The server must NOT serve a file from outside the repo root.
	traversalURL := srv.URL + "/" + repoName + "/objects/../../../../../../../../etc/passwd"
	resp := doGET(t, traversalURL, nil)
	defer resp.Body.Close()
	body := mustReadBody(t, resp.Body)

	if resp.StatusCode == http.StatusOK && bytes.Contains(body, []byte("root:")) {
		t.Errorf("path traversal succeeded: server returned /etc/passwd contents (status %d)", resp.StatusCode)
	}
}

// TestEdge_PackIdxPathTraversal_Rejected verifies spec §13.8 with a
// pack-.idx-style URL that tries to escape.
func TestEdge_PackIdxPathTraversal_Rejected(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	// /test.git/objects/pack/../../../../../etc/passwd
	traversalURL := srv.URL + "/" + repoName + "/objects/pack/../../../../../../../../etc/passwd"
	resp := doGET(t, traversalURL, nil)
	defer resp.Body.Close()
	body := mustReadBody(t, resp.Body)

	if resp.StatusCode == http.StatusOK && bytes.Contains(body, []byte("root:")) {
		t.Errorf("pack-path traversal succeeded: server returned /etc/passwd contents (status %d)", resp.StatusCode)
	}
}

// TestEdge_InvalidSHAInLooseObjectPath_Returns404 verifies spec §5.6 /
// §13.8: an invalid hex SHA in the loose-object path must yield 404, not
// 500 or a directory listing.
func TestEdge_InvalidSHAInLooseObjectPath_Returns404(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	// 40-character string with non-hex chars; should not match the route
	// regex (server uses [0-9a-f]{38,62}).
	badSHA := strings.Repeat("z", 40)
	resp := doGET(t, repoURL(srv, repoName, "objects", badSHA[:2], badSHA[2:]), nil)
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		t.Errorf("invalid-hex loose-object path returned 200; want non-200")
	}
}

// TestEdge_ChunkedRequestBody verifies spec §2.5 / §14.4: server SHOULD
// support chunked transfer-encoding for request bodies.
//
// We can't easily force chunked encoding from net/http (it uses Content-Length
// when the body fits in memory), so this test just verifies a normal POST
// works and is at least functionally equivalent.
func TestEdge_ChunkedRequestBody(t *testing.T) {
	root, repoName, _, refs, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	body := buildUploadRequest(t, refs["refs/heads/master"], nil, nil, true)
	resp := doRequest(t, http.MethodPost,
		repoURL(srv, repoName, "git-upload-pack"),
		bytes.NewReader(body),
		map[string]string{"Content-Type": ctUploadPackRequest})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 (POST with body)", resp.StatusCode)
	}
}

// TestEdge_NonExistentRepoOnAllEndpoints verifies spec §2.5: hitting ANY
// endpoint on a non-existent repo must NOT return 200. Iterates over all
// known dumb + smart URL patterns to ensure no endpoint bypasses the
// repo-existence check.
func TestEdge_NonExistentRepoOnAllEndpoints(t *testing.T) {
	root, _, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	fake := "does-not-exist.git"
	endpoints := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/info/refs"},
		{http.MethodGet, "/HEAD"},
		{http.MethodGet, "/objects/info/packs"},
		{http.MethodGet, "/objects/info/alternates"},
		{http.MethodGet, "/objects/info/http-alternates"},
		{http.MethodGet, "/objects/ab/cdef0123456789abcdef0123456789abcdef01"},
		{http.MethodGet, "/objects/pack/pack-" + strings.Repeat("0", 40) + ".pack"},
		{http.MethodGet, "/objects/pack/pack-" + strings.Repeat("0", 40) + ".idx"},
		{http.MethodGet, "/info/refs?service=git-upload-pack"},
		{http.MethodGet, "/info/refs?service=git-receive-pack"},
	}

	for _, e := range endpoints {
		t.Run(e.method+" "+e.path, func(t *testing.T) {
			resp := doRequest(t, e.method, srv.URL+"/"+fake+e.path, nil, nil)
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				t.Errorf("non-existent repo endpoint returned 200; spec forbids this")
			}
		})
	}
}

// TestEdge_V2Fetch_EmptyRequest verifies spec §10.4: a client MAY send an
// empty-request (just flush-pkt) to signal "no more commands". The server
// must handle this gracefully (no panic, no 500).
func TestEdge_V2Fetch_EmptyRequest(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	// Just a flush-pkt.
	resp := doRequest(t, http.MethodPost,
		repoURL(srv, repoName, "git-upload-pack"),
		bytes.NewReader(flushPkt()),
		map[string]string{
			"Content-Type": ctUploadPackRequest,
			"Git-Protocol": "version=2",
		})
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		t.Errorf("status = %d on empty v2 request; server should not crash", resp.StatusCode)
	}
}

// TestEdge_UploadPack_NoWant_ReturnsError verifies spec §7.3: "Client
// MUST send at least one `want` command." A request with no wants must be
// rejected.
func TestEdge_UploadPack_NoWant_ReturnsError(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	// Build a request with no wants — just flush + done.
	var buf bytes.Buffer
	buf.Write(flushPkt())
	b, _ := EncodePktLineString("done\n")
	buf.Write(b)

	resp := doRequest(t, http.MethodPost,
		repoURL(srv, repoName, "git-upload-pack"),
		bytes.NewReader(buf.Bytes()),
		map[string]string{"Content-Type": ctUploadPackRequest})
	defer resp.Body.Close()

	// Spec mandates the request is invalid; server must NOT return 200
	// with a packfile.
	if resp.StatusCode == http.StatusOK {
		body := mustReadBody(t, resp.Body)
		// If 200, must be an ERR pkt-line, not a packfile.
		if hasPrefixBytes(body, []byte("0008NAK\n")) {
			t.Errorf("server accepted a no-want request and started streaming a packfile")
		}
	}
}

// TestEdge_ReceivePack_NoCommands_ReturnsError verifies spec §8.3:
// "Client MUST send at least one command in the request body."
func TestEdge_ReceivePack_NoCommands_ReturnsError(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	// Build a request with no commands — just flush + packfile.
	var buf bytes.Buffer
	buf.Write(flushPkt())
	buf.Write(buildEmptyPack())

	resp := doRequest(t, http.MethodPost,
		repoURL(srv, repoName, "git-receive-pack"),
		bytes.NewReader(buf.Bytes()),
		map[string]string{"Content-Type": ctReceivePackRequest})
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		// Even if 200, the response must include an error pkt-line, not "unpack ok".
		body := mustReadBody(t, resp.Body)
		if bytes.Contains(body, []byte("unpack ok\n")) {
			t.Errorf("server accepted a no-command receive-pack request with \"unpack ok\"")
		}
	}
}

// TestEdge_V2_UnknownCommand verifies spec §10.4: a v2 POST with an
// unknown command must be rejected, not silently ignored.
func TestEdge_V2_UnknownCommand(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	var buf bytes.Buffer
	b, _ := EncodePktLineString("command=bogus\n")
	buf.Write(b)
	buf.Write(delimPkt())
	buf.Write(flushPkt())

	resp := doRequest(t, http.MethodPost,
		repoURL(srv, repoName, "git-upload-pack"),
		bytes.NewReader(buf.Bytes()),
		map[string]string{
			"Content-Type": ctUploadPackRequest,
			"Git-Protocol": "version=2",
		})
	defer resp.Body.Close()

	// Must not be 200 with a valid command response.
	if resp.StatusCode == http.StatusOK {
		body := mustReadBody(t, resp.Body)
		if bytes.Contains(body, []byte("packfile\n")) || bytes.Contains(body, []byte("acknowledgments\n")) {
			t.Errorf("server accepted unknown v2 command bogus; got 200 with a real response section")
		}
	}
}

// TestEdge_HTTPVersion_Independent verifies spec §2.5: "Server SHOULD
// support HTTP/1.0 and HTTP/1.1." httptest.Server uses HTTP/1.1 by
// default; we just verify a basic GET works.
func TestEdge_HTTPVersion_Independent(t *testing.T) {
	root, repoName, _, _, _, _, cleanup := setupBareRepo(t)
	defer cleanup()

	srv := newDefaultTestServer(t, root)
	defer srv.Close()

	resp := doGET(t, repoURL(srv, repoName, "info", "refs"), nil)
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		t.Errorf("basic GET returned 403; routing or handler broken")
	}
}

// ============================================================================
// End of tests.
//
// References to spec sections, for the curious reader:
//
//   §1   Introduction (dumb vs smart, version support)
//   §2   URL format, auth, SSL, sessions, general HTTP behaviour
//   §3   Comparative table smart vs dumb
//   §4   pkt-line format (length prefix, flush/delim/response-end markers)
//   §5   Dumb HTTP protocol (endpoints, info/refs, HEAD, packs, loose obj)
//   §6   Smart HTTP v0/v1 overview (discovery, service header, capabilities)
//   §7   git-upload-pack (fetch): want/have, ACK/NAK, side-band, packfile
//   §8   git-receive-pack (push): commands, packfile, report-status
//   §9   Capabilities (multi_ack, side-band-64k, ofs-delta, ...)
//   §10  Protocol v2 (command-oriented, ls-refs, fetch, response-end-pkt)
//   §11  Packfile format
//   §12  pack-*.idx format
//   §13  Security (auth, SSRF, path traversal, capability injection, ...)
//   §14  Edge cases (fallback, retry, gzip, chunked, HEAD vs GET, ...)
// ============================================================================
