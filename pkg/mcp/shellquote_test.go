package mcp

import "testing"

func TestShellQuote(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  string
	}{
		{"", "''"},
		{"abc-123_XYZ.456/:@=,", "abc-123_XYZ.456/:@=,"},
		{"hello world", "'hello world'"},
		{"don't", `'don'\''t'`},
		{"'quoted'", `''\''quoted'\'''`},
		{"$PATH", `'$PATH'`},
		{"`rm -rf /`", "'`rm -rf /`'"},
		{"line1\nline2", "'line1\nline2'"},
		{"foo; bar", "'foo; bar'"},
		{"a\"b", "'a\"b'"},
	} {
		if got := shellQuote(tc.input); got != tc.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
