package config

import (
	"fmt"
	"strconv"
	"strings"
)

// VSCode is the [vscode] table: where hpctun vscode-settings finds the
// site's settings, and whether it adds its built-in ones.
type VSCode struct {
	Settings string // site JSONC on the cluster: absolute or ~/...; "" for none
	Defaults bool   // add hpctun's built-in settings (default true)
}

type rawVSCode struct {
	Settings string `toml:"settings"`
	Defaults *bool  `toml:"defaults"`
}

func vscodeFromRaw(r *rawVSCode, present bool) (*VSCode, error) {
	if !present {
		return nil, nil
	}
	if r == nil {
		r = &rawVSCode{}
	}
	v := &VSCode{Settings: r.Settings, Defaults: r.Defaults == nil || *r.Defaults}
	bad := func(field, msg string) (*VSCode, error) {
		return nil, &ValidationError{Field: field, Msg: msg}
	}
	if s := v.Settings; s != "" {
		if strings.ContainsAny(s, "\r\n") {
			return bad("vscode.settings", "cannot contain line breaks")
		}
		if !strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "~/") {
			return bad("vscode.settings", "must be an absolute path or start with ~/ (a file on the cluster)")
		}
	}
	if !v.Defaults && v.Settings == "" {
		return bad("vscode.defaults", "false needs vscode.settings: there would be nothing to add")
	}
	return v, nil
}

// FormatVSCode renders v as a [vscode] table, leaving out defaults.
func FormatVSCode(v *VSCode) string {
	var b strings.Builder
	b.WriteString("[vscode]\n")
	if v.Settings != "" {
		fmt.Fprintf(&b, "settings = %s\n", strconv.Quote(v.Settings))
	}
	if !v.Defaults {
		b.WriteString("defaults = false\n")
	}
	return b.String()
}

func cloneVSCode(v *VSCode) *VSCode {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}
