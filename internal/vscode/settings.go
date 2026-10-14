package vscode

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
)

//go:embed defaults.jsonc
var defaultsText string

// Defaults returns hpctun's built-in machine settings.
func Defaults() map[string]any {
	m, err := ParseObject(defaultsText)
	if err != nil {
		panic("vscode: defaults.jsonc: " + err.Error())
	}
	return m
}

// ParseObject parses a JSONC settings object. Empty text, or only
// comments, is an empty object.
func ParseObject(text string) (map[string]any, error) {
	plain, _, err := scan(text)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(plain) == "" {
		return map[string]any{}, nil
	}
	var v any
	if err := json.Unmarshal([]byte(plain), &v); err != nil {
		var se *json.SyntaxError
		if errors.As(err, &se) {
			line := 1 + strings.Count(plain[:min(int(se.Offset), len(plain))], "\n")
			return nil, fmt.Errorf("line %d: %v", line, err)
		}
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("not a JSON object")
	}
	return m, nil
}

// Layer merges sources in order: a later value wins, objects merge entry
// by entry, and a null removes what earlier sources set. The result holds
// no nulls; the sources are not changed.
func Layer(sources ...map[string]any) map[string]any {
	res := map[string]any{}
	for _, s := range sources {
		res = overlay(res, s)
	}
	return res
}

func overlay(base, top map[string]any) map[string]any {
	res := make(map[string]any, len(base)+len(top))
	for k, v := range base {
		res[k] = v
	}
	for k, v := range top {
		switch tv := v.(type) {
		case nil:
			delete(res, k)
		case map[string]any:
			if bv, ok := res[k].(map[string]any); ok {
				res[k] = overlay(bv, tv)
			} else {
				res[k] = overlay(nil, tv)
			}
		default:
			res[k] = v
		}
	}
	return res
}

// AddSettings returns text with every setting in add that it lacks
// inserted as text, so the user's comments and order stay. For an object
// setting the user already has, only its missing entries are inserted.
// added lists what was inserted; skipped lists settings the user has with
// a different type than add's object.
func AddSettings(text string, add map[string]any) (out string, added, skipped []string, err error) {
	user, err := ParseObject(text)
	if err != nil {
		return "", nil, nil, err
	}
	_, open, _ := scan(text)
	type insert struct {
		at      int
		entries []string
	}
	var top []string
	var nested []insert
	for _, k := range slices.Sorted(maps.Keys(add)) {
		v := add[k]
		uv, has := user[k]
		if !has {
			top = append(top, entry(k, v, "\t"))
			added = append(added, k)
			continue
		}
		av, aObj := v.(map[string]any)
		if !aObj {
			continue
		}
		um, uObj := uv.(map[string]any)
		if !uObj {
			skipped = append(skipped, k)
			continue
		}
		var es []string
		for _, ek := range slices.Sorted(maps.Keys(av)) {
			if _, ok := um[ek]; !ok {
				es = append(es, entry(ek, av[ek], "\t\t"))
				added = append(added, k+" > "+ek)
			}
		}
		if len(es) > 0 {
			at := objectOpen(text, k)
			if at < 0 {
				return "", nil, nil, fmt.Errorf("cannot find %s in the file", k)
			}
			nested = append(nested, insert{at, es})
		}
	}
	if len(added) == 0 {
		return text, nil, skipped, nil
	}
	slices.Sort(added)
	var b strings.Builder
	switch {
	case open >= 0:
		// Inserts go in from the end so earlier offsets stay valid; the
		// outer brace comes before every nested one.
		slices.SortFunc(nested, func(x, y insert) int { return y.at - x.at })
		out = text
		for _, n := range nested {
			out = insertEntries(out, n.at, n.entries, "\t\t", "\t")
		}
		if len(top) > 0 {
			out = insertEntries(out, open, top, "\t", "")
		}
	default: // empty, or comments only: nothing nested can exist
		b.WriteString(text)
		if strings.TrimSpace(text) != "" && !strings.HasSuffix(text, "\n") {
			b.WriteByte('\n')
		}
		b.WriteString("{\n\t" + strings.Join(top, ",\n\t") + "\n}\n")
		out = b.String()
	}
	got, err := ParseObject(out)
	if err != nil {
		return "", nil, nil, fmt.Errorf("adding settings broke the file: %w", err)
	}
	for k, v := range user {
		if !reflect.DeepEqual(got[k], merged(v, add[k])) {
			return "", nil, nil, fmt.Errorf("adding settings changed %s", k)
		}
	}
	return out, added, skipped, nil
}

// insertEntries inserts entries into the object whose '{' ends at offset
// at, each on its own line indented by indent. A comment on the brace's
// line stays there: the entries go after it. In an empty object they take
// no trailing comma, and the '}' goes on its own line indented by
// closeIndent.
func insertEntries(text string, at int, entries []string, indent, closeIndent string) string {
	p, lineStart := at, false
	for i := at; i < len(text); {
		switch {
		case text[i] == ' ' || text[i] == '\t' || text[i] == '\r':
			i++
			continue
		case text[i] == '\n':
			p, lineStart = i+1, true
		case strings.HasPrefix(text[i:], "//"):
			if nl := strings.IndexByte(text[i:], '\n'); nl >= 0 {
				p, lineStart = i+nl+1, true
			}
		case strings.HasPrefix(text[i:], "/*"):
			end := strings.Index(text[i+2:], "*/")
			if end >= 0 && !strings.Contains(text[i+2:i+2+end], "\n") {
				i += 2 + end + 2
				p = i
				continue
			}
		}
		break
	}
	body := strings.Join(entries, ",\n"+indent)
	rest := text[p:]
	switch {
	case !emptyObject(rest) && lineStart:
		return text[:p] + indent + body + ",\n" + rest
	case !emptyObject(rest):
		return text[:p] + "\n" + indent + body + "," + rest
	case lineStart:
		return text[:p] + indent + body + "\n" + rest
	default:
		return text[:p] + "\n" + indent + body + "\n" + closeIndent + strings.TrimLeft(rest, " \t\r")
	}
}

// emptyObject reports whether rest, the text after an object's '{' (or
// after a comment on its line), holds only blanks and comments before
// the object's '}'.
func emptyObject(rest string) bool {
	for i := 0; i < len(rest); i++ {
		switch {
		case strings.IndexByte(" \t\r\n", rest[i]) >= 0:
		case strings.HasPrefix(rest[i:], "//"):
			for i < len(rest) && rest[i] != '\n' {
				i++
			}
		case strings.HasPrefix(rest[i:], "/*"):
			end := strings.Index(rest[i+2:], "*/")
			if end < 0 {
				return false
			}
			i += 2 + end + 1
		default:
			return rest[i] == '}'
		}
	}
	return false
}

// merged is the user's value u after its missing entries from a are added.
func merged(u, a any) any {
	um, ok1 := u.(map[string]any)
	am, ok2 := a.(map[string]any)
	if !ok1 || !ok2 {
		return u
	}
	res := maps.Clone(um)
	for k, v := range am {
		if _, ok := res[k]; !ok {
			res[k] = v
		}
	}
	return res
}

// entry renders "key": value, an object's lines indented by indent.
func entry(k string, v any, indent string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent(indent, "\t")
	enc.Encode(k)
	key := strings.TrimSuffix(b.String(), "\n")
	b.Reset()
	enc.Encode(v)
	return key + ": " + strings.TrimSuffix(b.String(), "\n")
}

// objectOpen returns the offset just after the '{' of top-level key k's
// object value in text, skipping strings and comments; -1 if not found.
func objectOpen(text, k string) int {
	depth := 0
	var lastKey string
	afterColon := false
	for i := 0; i < len(text); i++ {
		switch ch := text[i]; {
		case ch == '"':
			j := i + 1
			for ; j < len(text) && text[j] != '"'; j++ {
				if text[j] == '\\' {
					j++
				}
			}
			if depth == 1 && !afterColon {
				json.Unmarshal([]byte(text[i:j+1]), &lastKey)
			}
			afterColon = false
			i = j
		case ch == '/' && i+1 < len(text) && text[i+1] == '/':
			for i < len(text) && text[i] != '\n' {
				i++
			}
		case ch == '/' && i+1 < len(text) && text[i+1] == '*':
			i += 2 + strings.Index(text[i+2:], "*/") + 1
		case ch == ':':
			afterColon = depth == 1
		case ch == '{' || ch == '[':
			if ch == '{' && depth == 1 && afterColon && lastKey == k {
				return i + 1
			}
			depth++
			afterColon = false
		case ch == '}' || ch == ']':
			depth--
		case ch == ',':
			afterColon = false
		}
	}
	return -1
}
