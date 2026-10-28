package model_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestConsoleSessionTitleFromMessage(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, message, want string
	}{
		{"a short question is kept whole", "why does the merge queue stall?", "Why does the merge queue stall"},
		{"the first sentence of a paragraph", "Fix the off-by-one in the header parser. It drops the last field when the line ends without a newline.", "Fix the off-by-one in the header parser"},
		{"at most eight words", "I would like you to have a look at how the dispatcher chooses the next task please", "I would like you to have a look"},
		{"a leading code block is skipped", "```\ngo test ./...\n```\nthese tests are flaky", "These tests are flaky"},
		{"heading and list markers are stripped", "## Plan\n- rename the sessions page", "Plan"},
		{"a numbered list item", "1. check the nightly run", "Check the nightly run"},
		{"quoted lines are skipped", "> somebody said this\nwhat do you make of it", "What do you make of it"},
		{"a bare URL is dropped", "look at https://github.com/bwsalmon/grain/pull/1 and tell me what it changes", "Look at and tell me what it changes"},
		{"inline code keeps its words", "what does `reconcileConsoleSessions` do", "What does reconcileConsoleSessions do"},
		{"an empty message is untitled", "   \n\n", model.ConsoleSessionUntitled},
		{"a code block alone is untitled", "```\nls\n```", model.ConsoleSessionUntitled},
		{"trailing punctuation is dropped", "ship it!", "Ship it"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := model.ConsoleSessionTitleFromMessage(c.message); got != c.want {
				t.Errorf("ConsoleSessionTitleFromMessage(%q) = %q, want %q", c.message, got, c.want)
			}
		})
	}
}

func TestConsoleSessionTitleFromMessageIsBounded(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("antidisestablishmentarianism ", 8)
	got := model.ConsoleSessionTitleFromMessage(long)
	if n := utf8.RuneCountInString(got); n > model.ConsoleSessionTitleMaxLength {
		t.Errorf("title is %d runes, want at most %d: %q", n, model.ConsoleSessionTitleMaxLength, got)
	}
	if strings.HasSuffix(got, " ") || got == "" {
		t.Errorf("title %q should be cut at a word boundary and non-empty", got)
	}
	// One word longer than the whole bound is cut inside the word rather
	// than dropped to nothing.
	word := strings.Repeat("x", model.ConsoleSessionTitleMaxLength+10)
	if got := model.ConsoleSessionTitleFromMessage(word); utf8.RuneCountInString(got) != model.ConsoleSessionTitleMaxLength {
		t.Errorf("single long word: got %d runes, want %d", utf8.RuneCountInString(got), model.ConsoleSessionTitleMaxLength)
	}
}
