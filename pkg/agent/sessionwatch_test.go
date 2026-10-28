package agent

import (
	"bytes"
	"strings"
	"testing"
)

func TestSessionWatcherReportsEachNewSessionOnceAndPassesBytesThrough(t *testing.T) {
	extract := func(line []byte) string {
		s := string(line)
		if id, ok := strings.CutPrefix(s, "session="); ok {
			return id
		}
		return ""
	}
	for _, tc := range []struct {
		name   string
		writes []string
		want   []string
	}{
		{"one line in one write", []string{"session=a\n"}, []string{"a"}},
		{"a line split across writes", []string{"sess", "ion=a", "\nother\n"}, []string{"a"}},
		{"the same session again", []string{"session=a\nsession=a\n"}, []string{"a"}},
		{"a retry moves to another", []string{"session=a\nx\nsession=b\n"}, []string{"a", "b"}},
		{"nothing until the line ends", []string{"session=a"}, nil},
		{"lines naming nothing", []string{"x\ny\n"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var next bytes.Buffer
			var got []string
			w := NewSessionWatcher(&next, extract, func(s string) { got = append(got, s) })
			for _, p := range tc.writes {
				if n, err := w.Write([]byte(p)); err != nil || n != len(p) {
					t.Fatalf("Write(%q) = %d, %v", p, n, err)
				}
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("reported %q, want %q", got, tc.want)
			}
			if next.String() != strings.Join(tc.writes, "") {
				t.Errorf("passed through %q, want every byte unchanged", next.String())
			}
		})
	}
}

func TestSessionWatcherWithNothingToReportIsTheWriterItWraps(t *testing.T) {
	var next bytes.Buffer
	if w := NewSessionWatcher(&next, func([]byte) string { return "" }, nil); w != &next {
		t.Errorf("NewSessionWatcher with no report = %T, want the wrapped writer itself", w)
	}
	// And a nil next discards rather than panics.
	w := NewSessionWatcher(nil, func([]byte) string { return "s" }, func(string) {})
	if _, err := w.Write([]byte("line\n")); err != nil {
		t.Errorf("Write with nil next: %v", err)
	}
}
