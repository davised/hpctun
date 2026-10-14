package proc

import "testing"

func TestImageName(t *testing.T) {
	cases := map[string]string{
		`C:\Windows\System32\OpenSSH\ssh.exe`: "ssh",
		`C:\Program Files\Python\PYTHON.EXE`:  "python",
		"/usr/bin/ssh":                        "ssh",
		"ssh":                                 "ssh",
		`C:\tools\my.tool.exe`:                "my.tool",
	}
	for in, want := range cases {
		if got := ImageName(in); got != want {
			t.Errorf("ImageName(%q) = %q, want %q", in, got, want)
		}
	}
}
