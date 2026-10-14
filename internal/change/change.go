// Package change describes file edits hpctun proposes, shows them as unified
// diffs, and applies them once the user approves.
package change

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"hpctun/internal/atomicfile"
)

// Change is one proposed write.
type Change struct {
	Path    string      // file to write
	Display string      // how the diff names it, e.g. ~/.ssh/config
	Old     string      // current content; "" when the file does not exist
	Exists  bool        // the file exists now
	New     string      // proposed content
	Perm    fs.FileMode // mode for a new file (0644 when zero)
	DirPerm fs.FileMode // mode for missing parent directories (0755 when zero)
	Backup  bool        // copy Old to Path+".bak-hpctun" first, unless that exists
	Remove  bool        // delete Path instead of writing New
}

// Read returns a Change for path whose New equals its current content.
func Read(path, display string) (Change, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		return Change{Path: path, Display: display, Old: string(b), New: string(b), Exists: true}, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return Change{}, err
	}
	// ReadFile follows symlinks, so a dangling one (its target is gone)
	// looks the same as a missing file, although it still exists to
	// remove; Lstat decides existence instead.
	if _, err := os.Lstat(path); err == nil {
		return Change{Path: path, Display: display, Exists: true}, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Change{}, err
	}
	return Change{Path: path, Display: display}, nil
}

// Empty reports whether applying c would change nothing.
func (c Change) Empty() bool {
	return (c.Remove && !c.Exists) || (!c.Remove && c.Exists && c.Old == c.New)
}

// Apply writes each non-empty change in order, stopping at the first error.
// It first checks that every file is still as it was read (Old and Exists),
// and writes nothing if one changed since: the diffs shown for approval
// would no longer describe what gets written.
func Apply(cs []Change) error {
	for _, c := range cs {
		if c.Empty() {
			continue
		}
		now, err := Read(c.Path, c.Display)
		if err != nil {
			return err
		}
		if now.Exists != c.Exists || now.Old != c.Old {
			return fmt.Errorf("%s changed since the changes were shown, so nothing was written; run the command again to see the new changes", c.Display)
		}
	}
	for _, c := range cs {
		if c.Empty() {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(c.Path), cmp.Or(c.DirPerm, 0o755)); err != nil {
			return err
		}
		if c.Backup && c.Exists {
			bak := c.Path + ".bak-hpctun"
			if _, err := os.Lstat(bak); errors.Is(err, fs.ErrNotExist) {
				if err := os.WriteFile(bak, []byte(c.Old), 0o600); err != nil {
					return err
				}
			}
		}
		if c.Remove {
			if err := os.Remove(c.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			continue
		}
		if err := atomicfile.WriteFile(c.Path, []byte(c.New), cmp.Or(c.Perm, 0o644)); err != nil {
			return err
		}
	}
	return nil
}

// Diff is a unified diff from Old to New with three lines of context, or ""
// when nothing changes.
func (c Change) Diff() string {
	if c.Empty() {
		return ""
	}
	var b strings.Builder
	switch {
	case c.Remove:
		fmt.Fprintf(&b, "--- %s\n+++ /dev/null\n", c.Display)
	case c.Exists:
		fmt.Fprintf(&b, "--- %s\n+++ %s (proposed)\n", c.Display, c.Display)
	default:
		fmt.Fprintf(&b, "--- /dev/null\n+++ %s\n", c.Display)
	}
	after := lines(c.New)
	if c.Remove {
		after = nil
	}
	for _, h := range hunks(diffLines(lines(c.Old), after), 3) {
		b.WriteString(h)
	}
	return b.String()
}

func lines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

type op struct {
	kind byte // ' ', '-' or '+'
	text string
}

// diffLines is a longest-common-subsequence line diff; config files are
// small enough for the quadratic table.
func diffLines(a, b []string) []op {
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var ops []op
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, op{' ', a[i]})
			i, j = i+1, j+1
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, op{'-', a[i]})
			i++
		default:
			ops = append(ops, op{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, op{'-', a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, op{'+', b[j]})
	}
	return ops
}

// hunks groups changed lines with ctx lines of context; changes closer than
// 2*ctx share a hunk.
func hunks(ops []op, ctx int) []string {
	var out []string
	for i := 0; i < len(ops); {
		if ops[i].kind == ' ' {
			i++
			continue
		}
		start, end := max(i-ctx, 0), i
		for {
			for end < len(ops) && ops[end].kind != ' ' {
				end++
			}
			next := end
			for next < len(ops) && ops[next].kind == ' ' {
				next++
			}
			if next < len(ops) && next-end <= 2*ctx {
				end = next
				continue
			}
			break
		}
		stop := min(end+ctx, len(ops))
		aStart, bStart := 1, 1
		for _, o := range ops[:start] {
			if o.kind != '+' {
				aStart++
			}
			if o.kind != '-' {
				bStart++
			}
		}
		var aLen, bLen int
		var body strings.Builder
		for _, o := range ops[start:stop] {
			if o.kind != '+' {
				aLen++
			}
			if o.kind != '-' {
				bLen++
			}
			body.WriteByte(o.kind)
			body.WriteString(o.text)
			body.WriteByte('\n')
		}
		if aLen == 0 {
			aStart--
		}
		if bLen == 0 {
			bStart--
		}
		out = append(out, fmt.Sprintf("@@ -%d,%d +%d,%d @@\n", aStart, aLen, bStart, bLen)+body.String())
		i = stop
	}
	return out
}
