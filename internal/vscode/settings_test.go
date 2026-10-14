package vscode

import (
	"reflect"
	"strings"
	"testing"
)

func TestDefaults(t *testing.T) {
	d := Defaults()
	if d["search.followSymlinks"] != false {
		t.Errorf("search.followSymlinks = %v", d["search.followSymlinks"])
	}
	w, ok := d["files.watcherExclude"].(map[string]any)
	if !ok || w["**/.snakemake/**"] != true {
		t.Errorf("files.watcherExclude = %v", d["files.watcherExclude"])
	}
	d["x"] = 1
	if _, leaked := Defaults()["x"]; leaked {
		t.Error("Defaults shares its map")
	}
}

func TestParseObject(t *testing.T) {
	for _, s := range []string{"", "  \n", "// only\n/* c */"} {
		m, err := ParseObject(s)
		if err != nil || len(m) != 0 {
			t.Errorf("%q: %v %v", s, m, err)
		}
	}
	m, err := ParseObject("\uFEFF{ /* a\n b */ \"a\": 1, // c\n \"b\": {\"c\": null,},\n}")
	if err != nil || m["a"] != 1.0 || !reflect.DeepEqual(m["b"], map[string]any{"c": nil}) {
		t.Fatalf("%v %v", m, err)
	}
	for _, c := range []struct{ in, want string }{
		{"[1]", "not a JSON object"},
		{"{\n\"a\": 1,\n/* x\n y */\n\"b\" 2\n}", "line 5"},
		{"{\"a\": \"x", "unterminated string"},
	} {
		if _, err := ParseObject(c.in); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: got %v, want %q", c.in, err, c.want)
		}
	}
}

func TestLayer(t *testing.T) {
	base := map[string]any{
		"a": 1.0, "b": true, "keep": "x",
		"w": map[string]any{"**/work/**": true, "**/env/**": true},
	}
	site := map[string]any{
		"a":     2.0,                                             // override
		"b":     nil,                                             // remove a setting
		"w":     map[string]any{"**/work/**": nil, "**/x": true}, // remove and add entries
		"new":   map[string]any{"k": nil, "j": 1.0},              // nulls in a new object are dropped
		"ghost": nil,                                             // removing what is not there
	}
	got := Layer(base, site)
	want := map[string]any{
		"a": 2.0, "keep": "x",
		"w":   map[string]any{"**/env/**": true, "**/x": true},
		"new": map[string]any{"j": 1.0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	if base["a"] != 1.0 || len(base["w"].(map[string]any)) != 2 {
		t.Error("Layer changed its input")
	}
	if got := Layer(map[string]any{"a": nil}); len(got) != 0 {
		t.Errorf("a null in the first source: %v", got)
	}
	if got := Layer(); len(got) != 0 {
		t.Errorf("no sources: %v", got)
	}
}

func mustParse(t *testing.T, s string) map[string]any {
	t.Helper()
	m, err := ParseObject(s)
	if err != nil {
		t.Fatalf("%v\n%s", err, s)
	}
	return m
}

func TestAddSettings(t *testing.T) {
	user := "// mine\n{\n\t\"search.maxResults\": 50, // keep\n\t\"files.watcherExclude\": {\n\t\t\"**/mine/**\": true\n\t},\n\t\"search.exclude\": \"oops\"\n}\n"
	add := map[string]any{
		"search.maxResults":    1000.0,
		"search.searchOnType":  false,
		"files.watcherExclude": map[string]any{"**/mine/**": false, "**/work/**": true},
		"search.exclude":       map[string]any{"**/work": true},
		"files.exclude":        map[string]any{"**/.git": true},
	}
	out, added, skipped, err := AddSettings(user, add)
	if err != nil {
		t.Fatal(err)
	}
	wantAdded := []string{"files.exclude", "files.watcherExclude > **/work/**", "search.searchOnType"}
	if !reflect.DeepEqual(added, wantAdded) || !reflect.DeepEqual(skipped, []string{"search.exclude"}) {
		t.Fatalf("added %q skipped %q", added, skipped)
	}
	for _, keep := range []string{"// mine\n", "\"search.maxResults\": 50, // keep", "\"**/mine/**\": true"} {
		if !strings.Contains(out, keep) {
			t.Errorf("lost %q:\n%s", keep, out)
		}
	}
	got := mustParse(t, out)
	want := map[string]any{
		"search.maxResults":    50.0,
		"search.searchOnType":  false,
		"files.watcherExclude": map[string]any{"**/mine/**": true, "**/work/**": true},
		"search.exclude":       "oops",
		"files.exclude":        map[string]any{"**/.git": true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v\n%s", got, want, out)
	}
	again, added, _, err := AddSettings(out, add)
	if err != nil || again != out || len(added) != 0 {
		t.Errorf("second run changed it: %v %q", err, added)
	}
}

func TestAddSettingsShapes(t *testing.T) {
	add := map[string]any{"a": 1.0, "w": map[string]any{"x": true}}
	for _, in := range []string{"", "{}", "\uFEFF{\n}\n", "// only a comment\n", "{\"w\": {}}"} {
		out, added, _, err := AddSettings(in, add)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if got := mustParse(t, out); !reflect.DeepEqual(got, map[string]any{"a": 1.0, "w": map[string]any{"x": true}}) {
			t.Errorf("%q: got %v\n%s", in, got, out)
		}
		if len(added) == 0 {
			t.Errorf("%q: nothing added", in)
		}
		if strings.HasPrefix(in, "\uFEFF") && !strings.HasPrefix(out, "\uFEFF") {
			t.Errorf("%q: BOM lost", in)
		}
		if strings.HasPrefix(in, "//") && !strings.HasPrefix(out, "// only a comment\n") {
			t.Errorf("%q: comment lost:\n%s", in, out)
		}
	}
	if _, _, _, err := AddSettings("{\"a\": ", add); err == nil {
		t.Error("invalid JSONC accepted")
	}
}

func TestAddSettingsIgnoresKeysInCommentsAndStrings(t *testing.T) {
	user := "{\n\t// \"w\": {\n\t\"s\": \"\\\"w\\\": {\",\n\t\"w\": { /* { */ \"y\": true }\n}\n"
	out, added, _, err := AddSettings(user, map[string]any{"w": map[string]any{"x": true}})
	if err != nil || !reflect.DeepEqual(added, []string{"w > x"}) {
		t.Fatalf("%v %q", err, added)
	}
	got := mustParse(t, out)
	if !reflect.DeepEqual(got["w"], map[string]any{"x": true, "y": true}) || got["s"] != "\"w\": {" {
		t.Fatalf("got %v\n%s", got, out)
	}
}

func TestAddSettingsWritesValues(t *testing.T) {
	out, _, _, err := AddSettings("{}", map[string]any{"p": "a<b>&c", "o": map[string]any{"k": 1.0}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"a<b>&c"`) {
		t.Errorf("HTML-escaped:\n%s", out)
	}
	if !strings.Contains(out, "\"o\": {\n\t\t\"k\": 1\n\t}") {
		t.Errorf("object not indented:\n%s", out)
	}
}

// TestAddSettingsLayout pins the inserted text: an empty object gets its
// entries on their own lines with no trailing comma, and a comment after
// an opening brace stays on the brace's line.
func TestAddSettingsLayout(t *testing.T) {
	top := map[string]any{"a": 1.0, "w": map[string]any{"x": true}}
	z := map[string]any{"z": true}
	w := map[string]any{"w": map[string]any{"x": true}}
	for _, c := range []struct {
		in   string
		add  map[string]any
		want string
	}{
		{"{}", top, "{\n\t\"a\": 1,\n\t\"w\": {\n\t\t\"x\": true\n\t}\n}"},
		{"{}\n", top, "{\n\t\"a\": 1,\n\t\"w\": {\n\t\t\"x\": true\n\t}\n}\n"},
		{"{ }\n", z, "{\n\t\"z\": true\n}\n"},
		{"{\n}\n", top, "{\n\t\"a\": 1,\n\t\"w\": {\n\t\t\"x\": true\n\t}\n}\n"},
		{"{ // c\n}\n", z, "{ // c\n\t\"z\": true\n}\n"},
		{"{ /* c */ }\n", z, "{ /* c */\n\t\"z\": true\n}\n"},
		{"{ // c\n\t\"a\": 1\n}\n", z, "{ // c\n\t\"z\": true,\n\t\"a\": 1\n}\n"},
		{"{ /* c */\n\t\"a\": 1\n}\n", z, "{ /* c */\n\t\"z\": true,\n\t\"a\": 1\n}\n"},
		{"{ /* c */ \"a\": 1}", z, "{ /* c */\n\t\"z\": true, \"a\": 1}"},
		{"{ \"a\": 1}", z, "{\n\t\"z\": true, \"a\": 1}"},
		{"{\n\t\"w\": {}\n}\n", w, "{\n\t\"w\": {\n\t\t\"x\": true\n\t}\n}\n"},
		{"{\n\t\"w\": { // c\n\t\t\"y\": true\n\t}\n}\n", w, "{\n\t\"w\": { // c\n\t\t\"x\": true,\n\t\t\"y\": true\n\t}\n}\n"},
	} {
		out, _, _, err := AddSettings(c.in, c.add)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if out != c.want {
			t.Errorf("%q:\ngot  %q\nwant %q", c.in, out, c.want)
		}
	}
}
