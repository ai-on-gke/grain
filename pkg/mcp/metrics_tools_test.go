package mcp

// What read_grain_metrics asks for and what it does with the answer.
// The report itself is pkg/metrics' and pkg/ui's to get right and
// cmd/grain's to render, so what is only decidable here is the question:
// that every part of it the endpoint takes can be asked, that a section
// nobody has heard of is refused before a request is made, and that a
// report too long to carry back says how to ask a smaller question.

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeMetrics is a MetricsReader that records the question and answers
// with whatever it was told to.
type fakeMetrics struct {
	asked  MetricsQuery
	calls  int
	report string
	err    error
}

func (f *fakeMetrics) Metrics(_ context.Context, q MetricsQuery) (string, error) {
	f.asked = q
	f.calls++
	return f.report, f.err
}

func callMetrics(t *testing.T, reader MetricsReader, args map[string]any) Result {
	t.Helper()
	for _, tool := range NewMetricsTools(reader) {
		if tool.Name == "read_grain_metrics" {
			return tool.Handler(context.Background(), args)
		}
	}
	t.Fatalf("NewMetricsTools registered no read_grain_metrics")
	return Result{}
}

// The whole question the endpoint takes reaches the reader: a span said
// either way round, a scope on both attributes, and a section.
func TestReadMetricsPassesTheWholeQuestionThrough(t *testing.T) {
	t.Parallel()
	reader := &fakeMetrics{report: "window: ...\n\ntool use\n"}
	res := callMetrics(t, reader, map[string]any{
		"window":  "30d",
		"until":   "2026-09-01",
		"repo":    []any{"acme/widgets", "acme/docs"},
		"author":  "ada",
		"section": "tools",
	})
	if res.IsError {
		t.Fatalf("read_grain_metrics reported an error: %s", res.Text)
	}
	if reader.asked.Window != "30d" || reader.asked.Until != "2026-09-01" {
		t.Errorf("span asked for = %+v, want the window and the end date the call named", reader.asked)
	}
	// Repeatable both ways round, which is what argStrings is for: a
	// scope of one is a bare string and a scope of several an array.
	if got := strings.Join(reader.asked.Repos, ","); got != "acme/widgets,acme/docs" {
		t.Errorf("repos asked for = %q, want both of them", got)
	}
	if got := strings.Join(reader.asked.Authors, ","); got != "ada" {
		t.Errorf("authors asked for = %q, want the one the call named as a bare string", got)
	}
	if reader.asked.Section != MetricsSectionTools {
		t.Errorf("section asked for = %q, want %q", reader.asked.Section, MetricsSectionTools)
	}
	if res.Text != reader.report {
		t.Errorf("result = %q, want the report the reader rendered", res.Text)
	}
}

// A section nobody has heard of is a typo, and the answer is the list of
// the ones there are rather than a request for a report the caller did
// not want. Nothing is asked of the daemon: a wrong argument costs a
// round trip nowhere.
func TestReadMetricsRefusesASectionThatDoesNotExist(t *testing.T) {
	t.Parallel()
	reader := &fakeMetrics{report: "window: ...\n"}
	res := callMetrics(t, reader, map[string]any{"section": "toolz"})
	if !res.IsError {
		t.Fatalf("an unknown section was accepted: %q", res.Text)
	}
	if reader.calls != 0 {
		t.Errorf("the reader was asked %d time(s) for a section that does not exist", reader.calls)
	}
	for _, want := range []string{"toolz", MetricsSectionTools, MetricsSectionBacklog} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("refusal = %q, want it to mention %q", res.Text, want)
		}
	}
}

// Case and stray spaces are the caller's, not a different section.
func TestReadMetricsNormalizesTheSectionName(t *testing.T) {
	t.Parallel()
	reader := &fakeMetrics{report: "window: ...\n"}
	if res := callMetrics(t, reader, map[string]any{"section": " Tools "}); res.IsError {
		t.Fatalf("read_grain_metrics reported an error: %s", res.Text)
	}
	if reader.asked.Section != MetricsSectionTools {
		t.Errorf("section asked for = %q, want %q", reader.asked.Section, MetricsSectionTools)
	}
}

// A run with no daemon behind it gets the tool anyway -- so that every
// framework's allowedTools can name it -- and the refusal says which of
// the two things went wrong, since one is fixable by the caller and the
// other is not.
func TestReadMetricsWithNoReaderSaysWhyItCannotAnswer(t *testing.T) {
	t.Parallel()
	tools := NewMetricsTools(nil)
	if len(tools) != 1 || tools[0].Name != "read_grain_metrics" {
		t.Fatalf("NewMetricsTools(nil) registered %+v, want one read_grain_metrics", tools)
	}
	res := tools[0].Handler(context.Background(), nil)
	if !res.IsError || !strings.Contains(res.Text, "no route back to the grain daemon") {
		t.Errorf("result = %q, want a sentence about having no daemon to ask", res.Text)
	}
}

// An error from the daemon is the daemon's sentence, kept whole: a
// window nobody can parse is refused by the same code `grain metrics`
// refuses it with, and the caller can only fix it if they can read it.
func TestReadMetricsRelaysTheReadersRefusal(t *testing.T) {
	t.Parallel()
	reader := &fakeMetrics{err: errors.New(`window "banana": expected a duration like 36h`)}
	res := callMetrics(t, reader, map[string]any{"window": "banana"})
	if !res.IsError || !strings.Contains(res.Text, "expected a duration like 36h") {
		t.Errorf("result = %q, want the reader's own explanation", res.Text)
	}
}

// The report is a page of tables, so the cap can fire on it -- and the
// advice has to be actionable. "Narrow the request" (the generic
// sentence) is not: the two things that shorten a report are one
// section and less time, and both are named here.
func TestReadMetricsElisionAdviceNamesTheNarrowerQuestions(t *testing.T) {
	t.Parallel()
	tool := NewMetricsTools(nil)[0]
	advice := tool.elisionAdvice()
	for _, want := range []string{"section", "window"} {
		if !strings.Contains(advice, want) {
			t.Errorf("elision advice = %q, want it to mention %q", advice, want)
		}
	}
	if advice == elisionAdviceGeneric {
		t.Error("read_grain_metrics fell back to the generic elision advice")
	}

	// And the cap actually fires on an oversized report, carrying that
	// advice: a tool result that silently stopped mid-table would have a
	// run reading half a census as the whole of one.
	reader := &fakeMetrics{report: strings.Repeat("  run_command    1162  2163\n", 4000)}
	res := callMetrics(t, reader, nil)
	limit := int(maxToolResultBytes.Load())
	if len(res.Text) > limit+len(advice)+1024 {
		t.Errorf("result is %d bytes, want it capped near %d", len(res.Text), limit)
	}
	if !strings.Contains(res.Text, "section") {
		t.Errorf("a cut report does not say how to ask a smaller question:\n%s", res.Text)
	}
}

// Every section this package offers an agent has a sentence saying what
// is in it: a bare list of ten words is a list an agent guesses at.
func TestEveryMetricsSectionIsDescribed(t *testing.T) {
	t.Parallel()
	description := metricsSectionArgDescription()
	for _, name := range MetricsSections {
		if metricsSectionSummary[name] == "" {
			t.Errorf("section %q has no summary", name)
		}
		if !strings.Contains(description, name) {
			t.Errorf("the section argument's description does not mention %q", name)
		}
	}
}

// A run without the grant is told which box to have ticked, rather than
// that grain has no such tool: read_grain_metrics is registered only for
// a run holding self-debug, so "unknown tool" would send an agent
// looking for another way to read a census there is no other way to read
// (capabilityForTool, and the same answer main gave the self-upgrade
// tools).
func TestAnUngrantedMetricsCallNamesTheCapability(t *testing.T) {
	t.Parallel()
	registry := NewRegistry()
	client := NewInProcess(context.Background(), registry)
	t.Cleanup(func() { client.Close() })

	_, err := client.CallTool(context.Background(), "read_grain_metrics", nil)
	if err == nil {
		t.Fatal("calling read_grain_metrics on a server that does not serve it succeeded")
	}
	if !strings.Contains(err.Error(), "self-debug") {
		t.Errorf("the refusal does not name the capability that serves it: %v", err)
	}
}

// The tool has to say what the numbers are *of*. Every section counts
// the runs that recorded what it measures rather than every run in the
// window -- prompts have only been recorded since grain/task-268, the
// census since before it, sandboxes not at all over a window -- so a
// share read as "of the runs in this window" is read wrong, and an agent
// that will quote one has to be told to look at the denominator the
// header prints. See pkg/metrics/prompts.go's own "Which window to ask
// for".
func TestReadMetricsWarnsThatASectionsDenominatorIsNotTheWindow(t *testing.T) {
	t.Parallel()
	description := NewMetricsTools(nil)[0].Description
	for _, want := range []string{"denominator", "not every run in the window"} {
		if !strings.Contains(description, want) {
			t.Errorf("the tool's description does not mention %q: %s", want, description)
		}
	}
	if !strings.Contains(metricsSectionSummary[MetricsSectionPrompts], "recorded a prompt") {
		t.Errorf("the prompts section does not say what it counts: %s",
			metricsSectionSummary[MetricsSectionPrompts])
	}
}
