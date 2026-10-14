package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Source is where releases come from.
type Source interface {
	// Latest is the newest release's version.
	Latest(ctx context.Context) (string, error)
	// Fetch copies the named files of version into dir.
	Fetch(ctx context.Context, version string, names []string, dir string) error
}

// DefaultReleases is the public repository's releases page.
const DefaultReleases = "https://github.com/davised/hpctun/releases"

// Bounds on a download, so a server that stops talking cannot hang
// hpctun. Both are variables only so tests can shorten them.
var (
	// headerTimeout is how long to wait for response headers.
	headerTimeout = 30 * time.Second
	// idleTimeout is how long a body may deliver no bytes.
	idleTimeout = 60 * time.Second
)

// Bounds on a download's size, so a broken or hostile release cannot fill
// the disk. SHA256SUMS and its signature are a few lines; an archive is
// tens of megabytes. Variables only so tests can lower them.
var (
	maxSumsBytes    int64 = 64 << 10
	maxSigBytes     int64 = 1 << 10
	maxArchiveBytes int64 = 256 << 20
)

// maxAsset is the most bytes the release file name may have.
func maxAsset(name string) int64 {
	switch name {
	case "SHA256SUMS":
		return maxSumsBytes
	case SigName:
		return maxSigBytes
	}
	return maxArchiveBytes
}

// httpsOnly refuses a redirect to anything but https, so a download that
// starts on https can never continue in plain text.
func httpsOnly(req *http.Request, via []*http.Request) error {
	if req.URL.Scheme != "https" {
		return fmt.Errorf("refusing a redirect to %s: not https", req.URL.Redacted())
	}
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	return nil
}

// GitHubSource reads releases from a GitHub repository with plain HTTPS
// downloads, never the API, which allows only 60 unauthenticated requests
// an hour per IP address (a campus shares a few). Base is the releases URL
// (DefaultReleases); a nil Client gets the default transport with a bound
// on waiting for response headers.
type GitHubSource struct {
	Base   string
	Client *http.Client
}

func (s GitHubSource) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	// A fresh transport per call, rather than http.DefaultClient, so the
	// header bound applies and a test that shortens it is honoured: a
	// Fetch downloads only a few files, so the extra connection setup
	// is affordable.
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = headerTimeout
	return &http.Client{Transport: tr}
}

// idleBody wraps a download body: the timer passed in cancels the
// request when a Read delivers no bytes for idleTimeout, so a stalled
// download fails instead of hanging. Each Read that returns data
// restarts the timer.
type idleBody struct {
	body  io.Reader
	timer *time.Timer
}

func (b idleBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	if n > 0 {
		b.timer.Reset(idleTimeout)
	}
	return n, err
}

// get fetches url, which must answer 200; the caller closes the body.
func (s GitHubSource) get(ctx context.Context, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	// The redirect rule applies to a caller's client too (tests pass one).
	c := *s.client()
	c.CheckRedirect = httpsOnly
	res, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, res.Status)
	}
	return res.Body, nil
}

// Latest reads the VERSION asset of the latest release.
func (s GitHubSource) Latest(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	url := s.Base + "/latest/download/VERSION"
	body, err := s.get(ctx, url)
	if err != nil {
		return "", fmt.Errorf("can't check for updates at %s: %w", url, err)
	}
	defer body.Close()
	b, err := io.ReadAll(io.LimitReader(body, 256))
	if err != nil {
		return "", fmt.Errorf("can't check for updates at %s: %w", url, err)
	}
	v := strings.TrimSpace(string(b))
	if !ValidVersion(v) {
		return "", fmt.Errorf("unexpected VERSION at %s: %q", url, v)
	}
	return v, nil
}

// Fetch downloads each named asset of version into dir. A file appears
// under its name only once it is complete.
func (s GitHubSource) Fetch(ctx context.Context, version string, names []string, dir string) error {
	for _, n := range names {
		if err := s.fetchOne(ctx, version, n, dir); err != nil {
			return fmt.Errorf("downloading %s of %s: %w", n, version, err)
		}
	}
	return nil
}

func (s GitHubSource) fetchOne(ctx context.Context, version, name, dir string) error {
	// Cancelable per download, so the idle timer below can end a body
	// that stops delivering bytes.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	body, err := s.get(ctx, s.Base+"/download/"+version+"/"+name)
	if err != nil {
		return err
	}
	idle := time.AfterFunc(idleTimeout, cancel)
	defer idle.Stop()
	defer body.Close()
	tmp, err := os.CreateTemp(dir, "."+name+".")
	if err != nil {
		return err
	}
	limit := maxAsset(name)
	n, err := io.Copy(tmp, io.LimitReader(idleBody{body: body, timer: idle}, limit+1))
	if err == nil && n > limit {
		err = fmt.Errorf("%s is larger than %d bytes, more than a release file can be", name, limit)
	}
	if err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, name))
}
