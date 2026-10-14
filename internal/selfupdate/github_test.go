package selfupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// githubServer serves a fake GitHub releases tree:
// /releases/download/<ver>/<name> serves files[ver+"/"+name].
func githubServer(t *testing.T, files map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/releases/download/")
		body, ok := files[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestGitHubLatestFollowsRedirect follows a redirect to another host, as
// GitHub does: the releases URL answers 302 with a full URL on a second
// server (objects.githubusercontent.com there), which serves the asset.
// GitHub redirects within https.
func TestGitHubLatestFollowsRedirect(t *testing.T) {
	body := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/objects/VERSION" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte("v0.20261013-abc1234\n"))
	}))
	defer body.Close()
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/releases/latest/download/VERSION" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, body.URL+"/objects/VERSION", http.StatusFound)
	}))
	defer redirect.Close()
	got, err := GitHubSource{Base: redirect.URL + "/releases", Client: redirect.Client()}.Latest(context.Background())
	if err != nil || got != "v0.20261013-abc1234" {
		t.Fatalf("got %q, %v", got, err)
	}
}

// A redirect to plain http is refused: a download that starts on https
// never continues in plain text.
func TestGitHubRefusesHTTPRedirect(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("v0.20261013-abc1234\n"))
	}))
	defer plain.Close()
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/objects/VERSION", http.StatusFound)
	}))
	defer tls.Close()
	_, err := GitHubSource{Base: tls.URL + "/releases", Client: tls.Client()}.Latest(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not https") {
		t.Fatalf("err %v", err)
	}
}

func setBound[T any](t *testing.T, p *T, v T) {
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

func TestGitHubFetchRefusesOversizedAsset(t *testing.T) {
	setBound(t, &maxSumsBytes, 16)
	srv := githubServer(t, map[string]string{"v1/SHA256SUMS": strings.Repeat("x", 17), "v2/SHA256SUMS": strings.Repeat("x", 16)})
	dir := t.TempDir()
	src := GitHubSource{Base: srv.URL + "/releases"}
	err := src.Fetch(context.Background(), "v1", []string{"SHA256SUMS"}, dir)
	if err == nil || !strings.Contains(err.Error(), "larger than 16 bytes") {
		t.Fatalf("err %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "SHA256SUMS")); err == nil {
		t.Fatal("kept an oversized file")
	}
	if err := src.Fetch(context.Background(), "v2", []string{"SHA256SUMS"}, dir); err != nil {
		t.Fatalf("at the limit: %v", err)
	}
}

func TestGitHubLatestRejectsHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><body>Sign in to the network</body></html>"))
	}))
	defer srv.Close()
	_, err := GitHubSource{Base: srv.URL + "/releases"}.Latest(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unexpected VERSION") {
		t.Fatalf("err %v", err)
	}
}

func TestGitHubLatestNotFound(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	_, err := GitHubSource{Base: srv.URL + "/releases"}.Latest(context.Background())
	if err == nil || !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("err %v", err)
	}
}

func TestGitHubLatestOffline(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens there now
	_, err := GitHubSource{Base: url + "/releases"}.Latest(context.Background())
	if err == nil || !strings.Contains(err.Error(), "can't check for updates") || !strings.Contains(err.Error(), url) {
		t.Fatalf("err %v", err)
	}
}

func TestGitHubFetch(t *testing.T) {
	srv := githubServer(t, map[string]string{"v1/SHA256SUMS": "sums", "v1/a.tar.gz": "archive"})
	dir := t.TempDir()
	if err := (GitHubSource{Base: srv.URL + "/releases"}).Fetch(context.Background(), "v1", []string{"SHA256SUMS", "a.tar.gz"}, dir); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"SHA256SUMS": "sums", "a.tar.gz": "archive"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(b) != want {
			t.Errorf("%s: %q, %v", name, b, err)
		}
	}
}

func TestGitHubFetchMissingAsset(t *testing.T) {
	srv := githubServer(t, map[string]string{"v1/SHA256SUMS": "sums"})
	dir := t.TempDir()
	err := (GitHubSource{Base: srv.URL + "/releases"}).Fetch(context.Background(), "v1", []string{"SHA256SUMS", "a.tar.gz"}, dir)
	if err == nil || !strings.Contains(err.Error(), "a.tar.gz") || !strings.Contains(err.Error(), "v1") {
		t.Fatalf("err %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.tar.gz")); !os.IsNotExist(err) {
		t.Fatalf("a.tar.gz left behind: %v", err)
	}
}

// shortBound is the download bound the timeout tests use: long enough to
// be a real wait, short enough to keep each test under a second.
const shortBound = 200 * time.Millisecond

// shortenBounds shortens the download bounds for one test and puts the
// originals back afterwards.
func shortenBounds(t *testing.T) {
	t.Helper()
	header, idle := headerTimeout, idleTimeout
	headerTimeout, idleTimeout = shortBound, shortBound
	t.Cleanup(func() { headerTimeout, idleTimeout = header, idle })
}

// fetchWithin runs Fetch and fails the test if it has not returned
// within d, so a hang shows up as a failure in seconds rather than as
// the go test timeout.
func fetchWithin(t *testing.T, d time.Duration, fetch func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fetch() }()
	select {
	case err := <-done:
		return err
	case <-time.After(d):
		t.Fatalf("Fetch did not return within %s", d)
		return nil
	}
}

// wantEmptyDir asserts Fetch left nothing behind in dir: no file under
// the asset's name, and no .<name>.* temporary file.
func wantEmptyDir(t *testing.T, dir string) {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) > 0 {
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Fatalf("Fetch left %v in %s", names, dir)
	}
}

// TestGitHubFetchTimesOutWithoutResponseHeaders covers a server that
// accepts the connection and then says nothing.
func TestGitHubFetchTimesOutWithoutResponseHeaders(t *testing.T) {
	shortenBounds(t)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // no headers, no body
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) }) // before Close: it waits for the handler
	dir := t.TempDir()
	start := time.Now()
	err := fetchWithin(t, 2*time.Second, func() error {
		return (GitHubSource{Base: srv.URL + "/releases"}).Fetch(context.Background(), "v1", []string{"a.tar.gz"}, dir)
	})
	if elapsed := time.Since(start); elapsed < shortBound {
		t.Errorf("Fetch failed after %s, before the header timeout", elapsed)
	}
	if err == nil || !strings.Contains(err.Error(), "a.tar.gz") {
		t.Fatalf("err %v", err)
	}
	wantEmptyDir(t, dir)
}

// TestGitHubFetchTimesOutOnStalledBody covers a download that starts and
// then stops delivering bytes.
func TestGitHubFetchTimesOutOnStalledBody(t *testing.T) {
	shortenBounds(t)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("a few bytes"))
		w.(http.Flusher).Flush()
		<-release // then nothing more
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	dir := t.TempDir()
	start := time.Now()
	err := fetchWithin(t, 2*time.Second, func() error {
		return (GitHubSource{Base: srv.URL + "/releases"}).Fetch(context.Background(), "v1", []string{"a.tar.gz"}, dir)
	})
	if elapsed := time.Since(start); elapsed < shortBound {
		t.Errorf("Fetch failed after %s, before the idle timeout", elapsed)
	}
	if err == nil || !strings.Contains(err.Error(), "a.tar.gz") {
		t.Fatalf("err %v", err)
	}
	wantEmptyDir(t, dir)
}

func TestDefaultReleases(t *testing.T) {
	if DefaultReleases != "https://github.com/davised/hpctun/releases" {
		t.Fatal(DefaultReleases)
	}
}
