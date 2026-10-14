package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Bounds on what one archive may unpack to, so a broken or hostile release
// cannot fill the disk. Real archives are far below them: a few files of
// tens of megabytes. Variables only so tests can lower them.
var (
	maxMembers          = 1000
	maxFileBytes  int64 = 256 << 20
	maxTotalBytes int64 = 512 << 20
)

var errTooBig = errors.New("too big")

// unpacked counts what one extraction has written, against the bounds.
type unpacked struct{ total int64 }

func (u *unpacked) write(path, name string, r io.Reader, perm os.FileMode) error {
	limit := min(maxFileBytes, maxTotalBytes-u.total)
	n, err := writeFile(path, r, perm, limit)
	u.total += n
	if errors.Is(err, errTooBig) {
		return fmt.Errorf("%s unpacks to more than %d bytes", name, limit)
	}
	return err
}

// Extract copies members (paths inside archive, a .tar.gz or .zip) to
// dest/<base name>, executable. Every member must be a regular file there.
func Extract(archive string, members []string, dest string) error {
	want := map[string]bool{}
	for _, m := range members {
		want[m] = true
	}
	found := map[string]bool{}
	var u unpacked
	save := func(name string, r io.Reader) error {
		if !want[name] || found[name] {
			return nil
		}
		found[name] = true
		return u.write(filepath.Join(dest, path.Base(name)), name, r, 0o755)
	}
	var err error
	if strings.HasSuffix(archive, ".zip") {
		err = eachZipFile(archive, func(f *zip.File) error {
			if !f.Mode().IsRegular() {
				return nil
			}
			rc, err := f.Open()
			if err != nil {
				return err
			}
			defer rc.Close()
			return save(f.Name, rc)
		})
	} else {
		err = eachTarFile(archive, func(h *tar.Header, r io.Reader) error {
			if h.Typeflag != tar.TypeReg {
				return nil
			}
			return save(h.Name, r)
		})
	}
	if err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(archive), err)
	}
	for _, m := range members {
		if !found[m] {
			return fmt.Errorf("%s has no %s", filepath.Base(archive), m)
		}
	}
	return nil
}

// ExtractTree extracts the root/ tree of zipPath (an app bundle) into
// dest/root, keeping file modes. AppleDouble entries (._name, __MACOSX/)
// are skipped; absolute paths, .., entries outside root/ and anything but
// files and directories are errors.
func ExtractTree(zipPath, root, dest string) error {
	found := false
	var u unpacked
	err := eachZipFile(zipPath, func(f *zip.File) error {
		name := f.Name
		if strings.HasPrefix(name, "__MACOSX/") || strings.HasPrefix(path.Base(name), "._") {
			return nil
		}
		clean := path.Clean(name)
		if path.IsAbs(name) || strings.HasPrefix(name, "\\") || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(name, "/../") {
			return fmt.Errorf("unsafe path %q", name)
		}
		if clean != root && !strings.HasPrefix(clean, root+"/") {
			return fmt.Errorf("%q is outside %s", name, root)
		}
		target := filepath.Join(dest, filepath.FromSlash(clean))
		mode := f.Mode()
		switch {
		case mode.IsDir():
			found = true
			return os.MkdirAll(target, 0o755)
		case mode.IsRegular():
			found = true
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			rc, err := f.Open()
			if err != nil {
				return err
			}
			defer rc.Close()
			perm := mode.Perm()
			if perm == 0 {
				perm = 0o644
			}
			return u.write(target, name, rc, perm)
		default:
			return fmt.Errorf("%q is not a file or directory", name)
		}
	})
	if err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(zipPath), err)
	}
	if !found {
		return fmt.Errorf("%s has no %s", filepath.Base(zipPath), root)
	}
	return nil
}

// countReader counts the bytes a reader has delivered, so a reached cap
// can be told apart from a stream that merely ended.
type countReader struct {
	r io.Reader
	n int64
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// capped reports the stream cap when it was reached, and err otherwise, so
// a stream that was cut off is named as the cap rather than as the short
// read it looks like. Archive-level errors are named by the caller.
func (c *countReader) capped(err error) error {
	if c.n > maxTotalBytes {
		return fmt.Errorf("unpacks to more than %d bytes", maxTotalBytes)
	}
	return err
}

func eachZipFile(archive string, fn func(*zip.File) error) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	if len(zr.File) > maxMembers {
		return fmt.Errorf("more than %d entries", maxMembers)
	}
	for _, f := range zr.File {
		// The declared size of every entry is checked, extracted or not,
		// so an oversized one is rejected before anything reads it.
		if f.UncompressedSize64 > uint64(maxFileBytes) {
			return fmt.Errorf("%s unpacks to more than %d bytes", f.Name, maxFileBytes)
		}
		if err := fn(f); err != nil {
			return err
		}
	}
	return nil
}

func eachTarFile(archive string, fn func(*tar.Header, io.Reader) error) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	// The cap counts every decompressed byte, members nobody asked for
	// included, so a small download cannot force unbounded decompression
	// work while its entries are skipped. One byte past the cap is enough
	// to know the stream continues.
	cr := &countReader{r: io.LimitReader(gz, maxTotalBytes+1)}
	tr := tar.NewReader(cr)
	n := 0
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return cr.capped(nil) // the stream ended, or the cap cut it off
		}
		if err := cr.capped(err); err != nil {
			return err
		}
		// Every header is checked, extracted or not, so a member that is
		// too big is rejected before anything reads past its header.
		if h.Size > maxFileBytes {
			return fmt.Errorf("%s unpacks to more than %d bytes", h.Name, maxFileBytes)
		}
		if n++; n > maxMembers {
			return fmt.Errorf("more than %d entries", maxMembers)
		}
		if err := cr.capped(fn(h, tr)); err != nil {
			return err
		}
	}
}

func writeFile(path string, r io.Reader, perm os.FileMode, limit int64) (int64, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, io.LimitReader(r, limit+1))
	if err == nil && n > limit {
		err = errTooBig
	}
	if err != nil {
		f.Close()
		return n, err
	}
	if err := f.Close(); err != nil {
		return n, err
	}
	return n, os.Chmod(path, perm) // the umask may have narrowed it
}
