package config

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

const codeBase = "gateway = \"shell\"\n[ssh]\nuser = \"me\"\ntransfer = \"files\"\nnodes = [\"node*\"]\n"

func TestCodeDefaults(t *testing.T) {
	c, err := Parse([]byte(codeBase + "[code]\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := &Code{CPUs: 4, Time: "24:00:00", StartWithin: 30 * time.Minute, ServerDirs: []string{"/scratch", "/data"}}
	if !reflect.DeepEqual(c.Code, want) {
		t.Fatalf("got %+v", c.Code)
	}
}

func TestNoCodeTable(t *testing.T) {
	c, err := Parse([]byte(codeBase))
	if err != nil || c.Code != nil {
		t.Fatalf("got %+v, %v", c.Code, err)
	}
}

func TestCodeFields(t *testing.T) {
	c, err := Parse([]byte(codeBase + `[code]
cpus = 8
time = "2:00:00"
start_within = "45m"
partition = "core"
sbatch = ["--gres=gpu:1", "--comment=a b"]
server_dirs = ["/local/scratch"]
`))
	if err != nil {
		t.Fatal(err)
	}
	want := &Code{CPUs: 8, Time: "2:00:00", StartWithin: 45 * time.Minute, Partition: "core",
		Sbatch: []string{"--gres=gpu:1", "--comment=a b"}, ServerDirs: []string{"/local/scratch"}}
	if !reflect.DeepEqual(c.Code, want) {
		t.Fatalf("got %+v", c.Code)
	}
}

func TestCodeValidation(t *testing.T) {
	for _, c := range []struct{ text, field string }{
		{"gateway = \"shell\"\n[code]\n", "code"},
		{"gateway = \"shell\"\n[ssh]\ntransfer = \"files\"\n[code]\n", "ssh.user"},
		{codeBase + "[code]\ncpus = -1\n", "code.cpus"},
		{codeBase + "[code]\ntime = \"forever\"\n", "code.time"},
		{codeBase + "[code]\nstart_within = \"10s\"\n", "code.start_within"},
		{codeBase + "[code]\npartition = \"a b\"\n", "code.partition"},
		{codeBase + "[code]\nsbatch = [\"a\\nb\"]\n", "code.sbatch"},
		{codeBase + "[code]\nbogus = 1\n", "code.bogus"},
	} {
		_, err := Parse([]byte(c.text))
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.Field != c.field {
			t.Errorf("%q: err = %v, want field %s", c.text, err, c.field)
		}
	}
}

func TestFormatCodeRoundTrip(t *testing.T) {
	nc := NewConfig{Gateway: "shell", SSH: &SSH{User: "me", Nodes: []string{"node*"}},
		Code: &Code{CPUs: 8, Time: "2:00:00", StartWithin: 45 * time.Minute, Partition: "core",
			Sbatch: []string{"--gres=gpu:1", "a \"b\""}, ServerDirs: []string{"/local/a", "/data"}}}
	c, err := Parse([]byte(Render(nc)))
	if err != nil {
		t.Fatalf("%v\n%s", err, Render(nc))
	}
	if !reflect.DeepEqual(c.Code, nc.Code) {
		t.Fatalf("got %+v", c.Code)
	}
}

func TestFormatCodeDefaultsIsBareTable(t *testing.T) {
	d := &Code{CPUs: 4, Time: "24:00:00", StartWithin: 30 * time.Minute, ServerDirs: []string{"/scratch", "/data"}}
	if got := FormatCode(d); got != "[code]\n" {
		t.Fatalf("got %q", got)
	}
}

func TestFromConfigCopiesCode(t *testing.T) {
	c, err := Parse([]byte(codeBase + "[code]\nsbatch = [\"x\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	nc := FromConfig(c)
	nc.Code.Sbatch[0] = "changed"
	if c.Code.Sbatch[0] != "x" {
		t.Fatal("FromConfig shares Sbatch with the loaded config")
	}
}

func TestCodeServerDirs(t *testing.T) {
	base := "gateway = \"shell\"\n[ssh]\nuser = \"me\"\n[code]\n"
	c, err := Parse([]byte(base))
	if err != nil || !slices.Equal(c.Code.ServerDirs, []string{"/scratch", "/data"}) {
		t.Fatalf("default: %v %v", c, err)
	}
	c, err = Parse([]byte(base + "server_dirs = [\"/local/scratch\"]\n"))
	if err != nil || !slices.Equal(c.Code.ServerDirs, []string{"/local/scratch"}) {
		t.Fatalf("set: %v %v", c, err)
	}
	c, err = Parse([]byte(base + "server_dirs = []\n"))
	if err != nil || len(c.Code.ServerDirs) != 0 {
		t.Fatalf("empty (home only): %v %v", c, err)
	}
	for _, bad := range []string{`"scratch"`, `"/"`, `"/a b"`, `"/a;rm"`, `"/tmp"`} {
		if _, err := Parse([]byte(base + "server_dirs = [" + bad + "]\n")); err == nil || !strings.Contains(err.Error(), "code.server_dirs") {
			t.Errorf("%s: err %v", bad, err)
		}
	}
	if got := FormatCode(&Code{CPUs: DefaultCodeCPUs, Time: DefaultCodeTime, StartWithin: DefaultStartWithin, ServerDirs: []string{"/x"}}); got != "[code]\nserver_dirs = [\"/x\"]\n" {
		t.Fatalf("format %q", got)
	}
}

func TestCodeObsoleteKeys(t *testing.T) {
	c, err := Parse([]byte("gateway = \"shell\"\n[ssh]\nuser = \"me\"\n[code]\nalias = \"hpc-job\"\nhost_ca = \"ssh-ed25519 AAAA\"\n"))
	if err != nil || !slices.Equal(c.Code.Obsolete, []string{"alias", "host_ca"}) {
		t.Fatalf("%v %v", c, err)
	}
	if got := FormatCode(c.Code); strings.Contains(got, "alias") || strings.Contains(got, "host_ca") {
		t.Fatalf("format keeps obsolete keys: %q", got)
	}
}

// TestSeedCodeWithoutUser: a site file may carry [code] without ssh.user
// (the wizard asks for it); a config may not.
func TestSeedCodeWithoutUser(t *testing.T) {
	data := "gateway = \"shell\"\n[ssh]\ngateway_host = \"shell.example.org\"\n[code]\n"
	if c, err := parse([]byte(data), true); err != nil || c.Code == nil {
		t.Fatalf("seed: %+v %v", c, err)
	}
	if _, err := Parse([]byte(data)); err == nil || !strings.Contains(err.Error(), "ssh.user") {
		t.Fatalf("config: %v", err)
	}
}
