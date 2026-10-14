package config

import (
	"strings"
	"testing"
)

func TestVSCodeTable(t *testing.T) {
	c, err := Parse([]byte("gateway = \"g\"\n[vscode]\nsettings = \"/site/vscode.jsonc\"\n"))
	if err != nil || c.VSCode == nil || c.VSCode.Settings != "/site/vscode.jsonc" || !c.VSCode.Defaults {
		t.Fatalf("%+v %v", c.VSCode, err)
	}
	c, err = Parse([]byte("gateway = \"g\"\n[vscode]\nsettings = \"~/vs.jsonc\"\ndefaults = false\n"))
	if err != nil || c.VSCode.Defaults || c.VSCode.Settings != "~/vs.jsonc" {
		t.Fatalf("%+v %v", c.VSCode, err)
	}
	c, err = Parse([]byte("gateway = \"g\"\n[vscode]\n"))
	if err != nil || c.VSCode == nil || !c.VSCode.Defaults || c.VSCode.Settings != "" {
		t.Fatalf("empty table: %+v %v", c.VSCode, err)
	}
	c, err = Parse([]byte("gateway = \"g\"\n"))
	if err != nil || c.VSCode != nil {
		t.Fatalf("no table: %+v %v", c.VSCode, err)
	}
}

func TestVSCodeTableErrors(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{"settings = \"vs.jsonc\"", "vscode.settings: must be an absolute path or start with ~/"},
		{"settings = \"~bob/vs.jsonc\"", "vscode.settings: must be an absolute path or start with ~/"},
		{"settings = \"/a\\nb\"", "vscode.settings: cannot contain line breaks"},
		{"defaults = false", "vscode.defaults: false needs vscode.settings"},
		{"other = 1", "vscode.other: unknown field"},
	} {
		_, err := Parse([]byte("gateway = \"g\"\n[vscode]\n" + c.body + "\n"))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", c.body, err, c.want)
		}
	}
}

func TestVSCodeRenderRoundTrip(t *testing.T) {
	for _, v := range []*VSCode{{Defaults: true}, {Settings: "/s.jsonc", Defaults: true}, {Settings: "~/s.jsonc"}} {
		nc := NewConfig{Gateway: "g", VSCode: v}
		c, err := Parse([]byte(Render(nc)))
		if err != nil || c.VSCode == nil || *c.VSCode != *v {
			t.Errorf("%+v: got %+v %v\n%s", v, c.VSCode, err, Render(nc))
		}
		if got := FromConfig(c).VSCode; got == c.VSCode || *got != *v {
			t.Errorf("FromConfig: %+v (must be a copy)", got)
		}
	}
	if s := Render(NewConfig{Gateway: "g"}); strings.Contains(s, "[vscode]") {
		t.Errorf("rendered [vscode] without one:\n%s", s)
	}
}
