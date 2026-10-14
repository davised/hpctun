package vscode

import (
	"errors"
	"strings"
)

// scan turns text, JSON with comments and trailing commas (VS Code's
// settings format, possibly with a BOM), into plain JSON. open is the byte
// offset in text just after the first '{' outside strings and comments, or
// -1.
func scan(text string) (plain string, open int, err error) {
	s := strings.TrimPrefix(text, string([]byte{0xef, 0xbb, 0xbf}))
	off := len(text) - len(s)
	open = -1
	var b []byte
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == '"':
			j := i + 1
			for ; j < len(s) && s[j] != '"'; j++ {
				if s[j] == '\\' {
					j++
				}
			}
			if j >= len(s) {
				return "", -1, errors.New("unterminated string")
			}
			b = append(b, s[i:j+1]...)
			i = j
		case ch == '/' && i+1 < len(s) && s[i+1] == '/':
			for i < len(s) && s[i] != '\n' {
				i++
			}
			b = append(b, '\n')
		case ch == '/' && i+1 < len(s) && s[i+1] == '*':
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				return "", -1, errors.New("unterminated comment")
			}
			b = append(b, ' ')
			b = append(b, strings.Repeat("\n", strings.Count(s[i+2:i+2+end], "\n"))...)
			i += 2 + end + 1
		case ch == '}' || ch == ']':
			k := len(b)
			for k > 0 && strings.IndexByte(" \t\r\n", b[k-1]) >= 0 {
				k--
			}
			if k > 0 && b[k-1] == ',' {
				b = append(b[:k-1], b[k:]...)
			}
			b = append(b, ch)
		default:
			if ch == '{' && open < 0 {
				open = off + i + 1
			}
			b = append(b, ch)
		}
	}
	return string(b), open, nil
}
