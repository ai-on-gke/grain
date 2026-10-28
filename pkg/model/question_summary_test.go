package model

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestValidateQuestionSummary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		summary string
		ok      bool
	}{
		{"empty is allowed: it is derived instead", "", true},
		{"one line", "Keep the v1 API alongside v2, or remove it?", true},
		{"surrounding space is not counted", "  Pick a base branch  \n", true},
		{"exactly the cap", strings.Repeat("x", MaxQuestionSummaryLen), true},
		{"over the cap", strings.Repeat("x", MaxQuestionSummaryLen+1), false},
		{"counted in characters, not bytes", strings.Repeat("é", MaxQuestionSummaryLen), true},
		{"a second line", "Pick a base branch\nmain or release-1.4", false},
		{"a carriage return", "Pick a base branch\rmain", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateQuestionSummary(tc.summary)
			if (err == nil) != tc.ok {
				t.Fatalf("ValidateQuestionSummary(%q) = %v, want ok=%v", tc.summary, err, tc.ok)
			}
		})
	}
	if err := ValidateQuestionSummary("a\nb"); !errors.Is(err, ErrQuestionSummaryMultiline) {
		t.Errorf("a two-line summary: %v, want ErrQuestionSummaryMultiline", err)
	}
}

func TestNormalizeQuestionSummaryCollapsesAndCaps(t *testing.T) {
	if got := NormalizeQuestionSummary("  Pick\n a\tbase   branch "); got != "Pick a base branch" {
		t.Errorf("collapsed = %q", got)
	}
	long := strings.Repeat("word ", 60)
	got := NormalizeQuestionSummary(long)
	if n := utf8.RuneCountInString(got); n > MaxQuestionSummaryLen {
		t.Errorf("normalized summary is %d characters, want at most %d", n, MaxQuestionSummaryLen)
	}
	if !strings.HasSuffix(got, "…") || strings.Contains(got, "\n") {
		t.Errorf("normalized long summary = %q, want one line ending in an ellipsis", got)
	}
}

func TestDeriveQuestionSummary(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
	}{
		{"first sentence", "Should this target main? The release branch is frozen.", "Should this target main?"},
		{"first line when it has no sentence end", "Which base branch\n\nmain is frozen", "Which base branch"},
		{"skips blank lines and heading marks", "\n\n## Pick a base branch\n\nlong text", "Pick a base branch"},
		{"strips emphasis and code", "**Keep `v1`** or remove it? Details follow.", "Keep v1 or remove it?"},
		{"does not end a sentence inside a name", "Should pkg/model.Store keep v1.2 support? Yes or no.", "Should pkg/model.Store keep v1.2 support?"},
		{"ordered list marker", "1. Pick a base branch", "Pick a base branch"},
		{"bullet", "- Pick a base branch", "Pick a base branch"},
		{"empty", "  \n ", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DeriveQuestionSummary(tc.body); got != tc.want {
				t.Errorf("DeriveQuestionSummary(%q) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
	long := DeriveQuestionSummary(strings.Repeat("very ", 50) + "long question?")
	if n := utf8.RuneCountInString(long); n > MaxQuestionSummaryLen || !strings.HasSuffix(long, "…") {
		t.Errorf("a long first sentence derived %q (%d characters), want it cut to fit", long, n)
	}
}

func TestQuestionSummaryCountsFallbacks(t *testing.T) {
	before := QuestionSummaryFallbacks()
	if got := QuestionSummary(" Pick\na branch ", "body", "test"); got != "Pick a branch" {
		t.Errorf("given summary = %q, want it normalized", got)
	}
	if QuestionSummaryFallbacks() != before {
		t.Errorf("a given summary counted as a fallback")
	}
	if got := QuestionSummary("", "Which branch? Details.", "test"); got != "Which branch?" {
		t.Errorf("derived summary = %q", got)
	}
	if QuestionSummaryFallbacks() != before+1 {
		t.Errorf("fallbacks = %d, want %d", QuestionSummaryFallbacks(), before+1)
	}
}
