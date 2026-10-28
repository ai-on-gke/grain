package mcp

// capability_activity.go narrates the tool calls a run makes on its
// capability grants' MCP servers -- a gchat space's, a workspace server's, any server
// an operator attaches -- the way tool_activity.go narrates the calls it
// makes on grain's own.
//
// Those calls never reach a Registry. The agent CLI is configured with
// each grant's server beside grain-sandbox, and reaches it through a
// loopback proxy of grain's own (protocol.Proxy, wired in
// orchestrator.wireEndpoints) that carries the request to the server's
// data plane. So that proxy is where they are narrated: it names each
// tools/call it forwards to a CapabilityNarrator, which phrases it and
// puts the phrase on the run's row through the same StatusReporter
// every other line takes.
//
// Two things differ from the registry's own narration, both because the
// proxy is not the agent's process but the daemon's:
//
//   - The write is never on the call's path. The registry blocks a call
//     for at most toolActivityTimeout before running it; here a phrase is
//     handed to a goroutine and the call is forwarded at once, so a store
//     that is slow or locked costs the call nothing at all. Writes are
//     made in order, and a phrase still waiting when a newer one arrives
//     is dropped for it, so a burst of calls is one write behind at most.
//   - The arguments are the consumer's text, not grain's. A phrase that
//     names one (a path, a pattern) has it flattened onto one line,
//     stripped of control characters, cut short, and left out entirely if
//     it looks like a credential (activityArg).

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/bwsalmon/grain/pkg/redact"
)

// maxActivityArg is the most of one argument a phrase quotes.
const maxActivityArg = 60

// CapabilityNarrator puts what a run is doing on its capability servers
// on its task's row. One per run: every server the run was granted
// reports through the same one, so their phrases are written in the
// order the calls were made.
type CapabilityNarrator struct {
	reporter StatusReporter

	mu      sync.Mutex
	pending string
	writing bool
}

// NewCapabilityNarrator returns a narrator writing through reporter, or
// nil -- which narrates nothing -- for a nil reporter.
func NewCapabilityNarrator(reporter StatusReporter) *CapabilityNarrator {
	if reporter == nil {
		return nil
	}
	return &CapabilityNarrator{reporter: reporter}
}

// Observer is what a proxy of one server is to call with each tools/call
// it forwards (protocol.ToolCallObserver's shape). server is the name the
// agent knows the server by, reported the serverInfo.name it gave itself
// -- which is what picks a built-in server's phrases -- and labels each
// tool's own title or description (protocol.EndpointRoster.Labels).
func (n *CapabilityNarrator) Observer(server, reported string, labels map[string]string) func(tool string, args map[string]any) {
	return func(tool string, args map[string]any) {
		if n != nil {
			n.say(capabilityActivityNote(server, reported, labels, tool, args))
		}
	}
}

// say queues note to be written, replacing any note still waiting, and
// starts the writer if it is not already running.
func (n *CapabilityNarrator) say(note string) {
	if note == "" {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.pending = note
	if !n.writing {
		n.writing = true
		go n.write()
	}
}

// write drains pending, one bounded write at a time, and exits once there
// is nothing left -- so a narrator owns no goroutine while its run is
// quiet, and needs no Close.
func (n *CapabilityNarrator) write() {
	for {
		n.mu.Lock()
		note := n.pending
		n.pending = ""
		if note == "" {
			n.writing = false
			n.mu.Unlock()
			return
		}
		n.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), toolActivityTimeout)
		_, _ = n.reporter.ReportStatus(ctx, note)
		cancel()
	}
}

// capabilityActivityNote is the phrase for one call to tool on server:
// the built-in phrase where server is one grain ships
// (capabilityActivityNotes), else the tool's own label, else its name --
// always after the server's name, so a line can never pass for one of
// grain's own (IsDeclaredWait reads those).
func capabilityActivityNote(server, reported string, labels map[string]string, tool string, args map[string]any) string {
	server = activityText(server)
	if server == "" {
		server = "a capability server"
	}
	phrase := ""
	if note, ok := capabilityToolNote(builtinCapabilityServer(reported), tool); ok {
		phrase = note.render(args)
	}
	if phrase == "" {
		phrase, _ = activityArg(labels[tool], MaxStatusLength, false)
	}
	if phrase == "" {
		phrase, _ = activityArg(tool, maxActivityArg, false)
	}
	if phrase == "" {
		phrase = "calling a tool"
	}
	return clipActivity(server+": "+phrase, MaxStatusLength)
}

// builtinCapabilityServer is which of capabilityActivityNotes' servers
// reported is: its own name, except a gchat grant's, which is named for
// its space (intree.GChatName + "-" + space).
func builtinCapabilityServer(reported string) string {
	if strings.HasPrefix(reported, "gchat-") {
		return "gchat"
	}
	return reported
}

// CapabilityToolNote is one built-in tool's phrase: Format, with each %s
// filled by one of Args in turn, or Fallback when any of them is absent
// or not fit to show.
type CapabilityToolNote struct {
	Format   string
	Args     []NoteArg
	Fallback string
}

// NoteArg is one argument a phrase names, and how it is shown.
type NoteArg struct {
	Key  string
	Show func(string) string
	// Tail keeps the end of a long value rather than its start: the file
	// at the end of a path is the part a reader wants.
	Tail bool
}

// QuotedArg is an argument shown in quotes: a pattern, a message.
func QuotedArg(key string) NoteArg {
	return NoteArg{Key: key, Show: func(s string) string { return `"` + s + `"` }}
}

func (c CapabilityToolNote) render(args map[string]any) string {
	if len(c.Args) == 0 {
		return c.Fallback
	}
	parts := make([]any, len(c.Args))
	for i, a := range c.Args {
		v, ok := activityArg(args[a.Key], maxActivityArg, a.Tail)
		if !ok {
			return c.Fallback
		}
		if a.Show != nil {
			v = a.Show(v)
		}
		parts[i] = v
	}
	out := c.Format
	for _, p := range parts {
		out = strings.Replace(out, "%s", p.(string), 1)
	}
	return out
}

// longToken is a run of characters with nothing in it to read, the
// length of a key or a token: what a credential pasted into an argument
// looks like when it has no shape pkg/redact knows by name.
var longToken = regexp.MustCompile(`[A-Za-z0-9+=]{32,}`)

// activityArg is v as a phrase may show it, and whether it may: a string
// or a whole number, flattened onto one line with control and formatting
// characters taken out, and cut to max runes. Anything that looks like a
// credential is refused rather than cut -- a phrase is shown to everybody
// who can see the task list.
func activityArg(v any, max int, tail bool) (string, bool) {
	var s string
	switch x := v.(type) {
	case string:
		s = x
	case float64:
		if x != float64(int64(x)) {
			return "", false
		}
		s = strconv.FormatInt(int64(x), 10)
	default:
		return "", false
	}
	s = activityText(s)
	if s == "" {
		return "", false
	}
	if _, n := (*redact.Redactor)(nil).Line(s); n > 0 {
		return "", false
	}
	for _, tok := range longToken.FindAllString(s, -1) {
		if strings.ContainsAny(tok, "0123456789") && strings.ContainsFunc(tok, unicode.IsLetter) {
			return "", false
		}
	}
	if utf8.RuneCountInString(s) > max {
		r := []rune(s)
		if tail {
			s = "…" + strings.TrimLeft(string(r[len(r)-(max-1):]), " ")
		} else {
			s = strings.TrimRight(string(r[:max-1]), " ") + "…"
		}
	}
	return s, true
}

// activityText is s with every control and formatting character (a
// newline, an escape, a bidi override) made a space, and whitespace
// collapsed onto one line.
func activityText(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == utf8.RuneError || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}
