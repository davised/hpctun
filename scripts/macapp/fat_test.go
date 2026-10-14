package main

import (
	"bytes"
	"debug/macho"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// buildHello cross-builds testdata/hello for darwin/arch.
func buildHello(t *testing.T, arch string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "hello-"+arch)
	cmd := exec.Command("go", "build", "-trimpath", "-o", out, "./testdata/hello")
	cmd.Env = append(os.Environ(), "GOOS=darwin", "GOARCH="+arch, "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, b)
	}
	return out
}

func TestMakeFatHoldsBothSlices(t *testing.T) {
	arm, amd := buildHello(t, "arm64"), buildHello(t, "amd64")
	data, err := makeFat(arm, amd)
	if err != nil {
		t.Fatal(err)
	}
	ff, err := macho.NewFatFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(ff.Arches) != 2 || ff.Arches[0].Cpu != macho.CpuAmd64 || ff.Arches[1].Cpu != macho.CpuArm64 {
		t.Fatalf("slices: %+v, want x86_64 then arm64", ff.Arches)
	}
	for _, a := range ff.Arches {
		in := map[macho.Cpu]string{macho.CpuAmd64: amd, macho.CpuArm64: arm}[a.Cpu]
		want, _ := os.ReadFile(in)
		if got := data[a.Offset : a.Offset+a.Size]; !bytes.Equal(got, want) {
			t.Errorf("%v slice differs from %s", a.Cpu, in)
		}
		if a.Offset%(1<<a.Align) != 0 {
			t.Errorf("%v slice at %d is not aligned to 2^%d", a.Cpu, a.Offset, a.Align)
		}
	}
}

func TestMakeFatInputOrderDoesNotMatter(t *testing.T) {
	arm, amd := buildHello(t, "arm64"), buildHello(t, "amd64")
	a, err := makeFat(arm, amd)
	if err != nil {
		t.Fatal(err)
	}
	b, err := makeFat(amd, arm)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Error("makeFat(arm, amd) != makeFat(amd, arm)")
	}
}

func TestMakeFatRejects(t *testing.T) {
	arm, amd := buildHello(t, "arm64"), buildHello(t, "amd64")
	text := filepath.Join(t.TempDir(), "notes.txt")
	os.WriteFile(text, []byte("not a program"), 0o644)
	fat := filepath.Join(t.TempDir(), "fat")
	data, err := makeFat(arm, amd)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(fat, data, 0o755)
	for name, tc := range map[string]struct {
		paths []string
		want  string
	}{
		"not mach-o": {[]string{arm, text}, "notes.txt: not a thin Mach-O file"},
		"fat input":  {[]string{fat, amd}, "fat: not a thin Mach-O file"},
		"same cpu":   {[]string{arm, arm}, "a second"},
		"one input":  {[]string{arm}, "need two thin Mach-O files"},
		"missing":    {[]string{arm, filepath.Join(t.TempDir(), "nope")}, "nope"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := makeFat(tc.paths...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestMakeFatMatchesLipo(t *testing.T) {
	lipo, err := exec.LookPath("lipo")
	if err != nil {
		t.Skip("no lipo here (it comes with Xcode's tools on a Mac)")
	}
	arm, amd := buildHello(t, "arm64"), buildHello(t, "amd64")
	want := filepath.Join(t.TempDir(), "lipo")
	if b, err := exec.Command(lipo, "-create", "-output", want, arm, amd).CombinedOutput(); err != nil {
		t.Fatalf("lipo: %v\n%s", err, b)
	}
	got, err := makeFat(arm, amd)
	if err != nil {
		t.Fatal(err)
	}
	if w, _ := os.ReadFile(want); !bytes.Equal(got, w) {
		t.Errorf("makeFat differs from lipo -create (%d vs %d bytes)", len(got), len(w))
	}
}

func TestMakeFatRunsAndStaysSigned(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("runs the universal program, which needs a Mac")
	}
	data, err := makeFat(buildHello(t, "arm64"), buildHello(t, "amd64"))
	if err != nil {
		t.Fatal(err)
	}
	prog := filepath.Join(t.TempDir(), "hello")
	if err := os.WriteFile(prog, data, 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(prog).Output()
	if err != nil || strings.TrimSpace(string(out)) != "hello from "+runtime.GOARCH {
		t.Fatalf("running it: %q, %v", out, err)
	}
	if b, err := exec.Command("codesign", "--verify", "--arch", "arm64", prog).CombinedOutput(); err != nil {
		t.Errorf("codesign --verify --arch arm64: %v\n%s", err, b)
	}
}
