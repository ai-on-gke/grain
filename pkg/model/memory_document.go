package model

import (
	"fmt"
	"strings"
	"time"
)

// MemoryDocument is a memory too long to carry in every prompt: a named
// note, per scope, that a run reads when it turns out to matter
// (grain/task-270).
//
// Memory next door (memory.go) is a block of prose appended to every
// dispatch this deployment makes, and MaxMemoryLength is what keeps it
// honest -- 8 KiB per scope, because the cost of a memory lands on every
// run afterwards, forever. That bound is the whole reason the injected
// memory is worth reading, and it is also its limit: an agent that has
// worked out why the e2e suite fails on its third retry has half a page
// to say it in, and the choice is to spend the whole deployment's budget
// on one repo's flake or to write a sentence that gestures at something
// nobody can look up.
//
// A document is where the rest goes. The injected memory keeps the
// sentence -- "the e2e suite fails for its own reasons on the third
// retry; there is a note about it" -- and MemoryIndex turns the
// documents that exist into one line each in the same block, so a run is
// told what is knowable without being made to read it. The reading is a
// tool call (read_memory_document), made by the run that has decided the
// note bears on what it is doing.
//
// # Rows in the store, not files in the state repository
//
// grain/task-237's own follow-up left this open: a document could as
// easily have been a real file in the state repository, readable with
// the file tools every run already has, which would have made a read
// tool unnecessary for any run that can clone it. It is a row, like the
// memory beside it, for three reasons that all point the same way.
//
// Almost no run has the state repository in front of it. A run works in
// the repository its task targets -- kontur, bazzite, somebody's service
// -- and the state repository is checked out only for a task that
// targets *it*. The runs a document exists for are exactly the ones
// that would have to clone a second repository, holding this
// deployment's entire history, to read half a page about the one they
// are in.
//
// The state repository is a dump, and its contract runs the other way.
// Every file under tables/ is rewritten from this database on a timer
// (pkg/staterepo), which is what makes it diffable, restorable and
// reviewable; a document living there as a file of its own would be the
// one file in it with no table behind it, and the export's "an unchanged
// database produces identical bytes" property would have nothing to say
// about it. As a row it is still a file -- tables/memory_document.json,
// on every sync -- so the reviewability that made the file idea
// attractive is had anyway, without inverting where the truth is.
//
// And a file in a git repository is written by a commit. Memory is
// written by an agent mid-run, through the daemon's own REST API, with
// no credential in the sandbox and no assumption that a remote exists at
// all (a local-only deployment's state repository has no origin). Making
// a document a file would mean either an agent that can push to this
// deployment's settings or a daemon committing on its behalf -- which is
// what the export already is. Worse, a file in the repository is a file
// a merged pull request can replace, and memory is deliberately off
// staterepo.SettingsTables precisely so that a branch cut before last
// night's memory agent ran cannot silently delete what it learned.
//
// # Staleness
//
// Nothing reads a document on most runs, so nothing notices when one
// stops being true. There is no automatic answer here and no expiry:
// what there is is that every index line carries the document's age
// (MemoryIndex), the read reports it again, and the same "memory"
// capability gates reading and writing -- so any run that can read a
// document can correct it in the same turn, by replacing it whole. That
// is the same bet update_memory makes and no more: a note is a run's
// observation, and the run in front of the tree is the one qualified to
// say it has gone stale.
type MemoryDocument struct {
	// Repo is which scope this document is filed under, or nil for the
	// deployment's own. Stored as the (owner, name) key, the way Memory's
	// own Repo is.
	Repo *RepoRef
	// Name is what a run asks for it by: a short slug, unique within its
	// scope, validated by ValidateMemoryDocumentName. It is the only
	// handle there is -- there is no id -- so writing the same name twice
	// replaces the document rather than filing a second one.
	Name string
	// Summary is the one line the index carries: what this document is
	// about, written so a run can tell whether it is worth a tool call.
	// It is the part that costs every prompt, which is why it is bounded
	// far tighter than the text and why it is required.
	Summary string
	// Text is the document itself, replaced wholesale on every write.
	// Empty is how a document is deleted, exactly as an empty memory
	// forgets a scope.
	//
	// Not read by a listing: ListMemoryDocuments leaves it empty and
	// fills Length instead, since the index needs every document's name
	// and nobody's text.
	Text string
	// Length is how many bytes Text holds, as reported by a listing that
	// did not read it. Zero from any path that carries the text itself --
	// len(Text) is the answer there, and a field that shadowed it would
	// be one more thing to keep true.
	Length int
	// UpdatedAt is when it was last written, which is the whole of what a
	// reader has to judge staleness by (see the type's doc comment).
	UpdatedAt time.Time
	// UpdatedBy is who last wrote it, exactly as Memory.UpdatedBy is who
	// last wrote the memory beside it -- an agent through update_memory,
	// a person through the Settings pane, or nobody known for a row
	// written before the column existed. That field's doc comment has
	// the whole of why it is recorded and on what footing.
	//
	// It reaches a prompt the same way the age does: as a clause on this
	// document's index line (MemoryIndex), for a run deciding whether
	// what it is about to read is an earlier run's observation or
	// somebody's own correction.
	UpdatedBy Principal
}

const (
	// MaxMemoryDocumentLength bounds one document. Two orders of
	// magnitude above MaxMemoryLength, because the two are bounded for
	// opposite reasons: the injected memory is bounded so that every
	// later run pays little, and a document is bounded only so that the
	// one run that asks for it gets an answer it can read in a turn. 64
	// KiB is a long chapter -- past the point where a note has stopped
	// being a note -- and is the size at which "put the detail in the
	// tasks and the state repository" starts being the better advice
	// again.
	MaxMemoryDocumentLength = 64 * 1024
	// MaxMemoryDocumentSummaryLength bounds the index line, which is the
	// part every dispatch carries. One sentence: enough to say what the
	// document is about, too little to say what it says.
	MaxMemoryDocumentSummaryLength = 200
	// MaxMemoryDocumentsPerScope bounds how many documents one scope may
	// have, and so how long the index in every prompt can grow. It is the
	// same pressure MaxMemoryLength applies to the memory itself, applied
	// to the thing that replaced it: without it an agent would file a
	// document per observation and hand every later run a page of
	// titles -- which is the diary the 8 KiB limit exists to prevent,
	// rebuilt out of summaries.
	MaxMemoryDocumentsPerScope = 24
	// MaxMemoryDocumentNameLength bounds the slug. A name is a handle a
	// model types back into a tool call, not a title.
	MaxMemoryDocumentNameLength = 64
)

// ValidateMemoryDocumentName accepts the slug a document is filed and
// asked for under: lower-case letters, digits and single hyphens
// between them, like "flaky-e2e" or "kontur-vm-boot".
//
// Narrow on purpose. The name is a key in the store, a word in a tool
// call and a word in every prompt the index appears in, and the three
// have to be the same string: a name that differed by case, by a
// trailing space or by an underscore somebody guessed at would be a
// document that exists and cannot be read.
func ValidateMemoryDocumentName(name string) error {
	if name == "" {
		return fmt.Errorf("a memory document needs a name")
	}
	if len(name) > MaxMemoryDocumentNameLength {
		return fmt.Errorf("%q is %d characters; a document name is at most %d",
			name, len(name), MaxMemoryDocumentNameLength)
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") || strings.Contains(name, "--") {
		return fmt.Errorf("%q: hyphens in a document name separate words, so it starts and ends with one of them", name)
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return fmt.Errorf("%q: a document name is lower-case letters, digits and hyphens -- "+
				"it is typed back into a tool call, so it stays something that can be typed", name)
		}
	}
	return nil
}

// MemoryIndex is how documents reach a prompt: one line each, naming the
// document, its scope, its age, who wrote it when that was a person, and
// the summary whoever wrote it left -- and nothing else, because the
// point of a document is that its contents are not here.
//
// "written by hand" is said only of a document a person wrote
// (MemoryHandNote has the reasoning, and this is its one-clause form):
// an agent's is what a run already assumes it is reading, and a row from
// before the author was recorded is unknown, which is neither.
//
// docs are rendered in the order given, which is the order
// Store.ListMemoryDocuments returns (the deployment's own first, then
// each repo, each in name order) -- a stable order, so the same
// documents produce the same prompt twice.
//
// "" for no documents at all, which is every deployment until something
// has written one: an index with nothing in it would be a paragraph of
// prompt saying there is nothing to look up.
func MemoryIndex(docs []MemoryDocument, now time.Time) string {
	var lines []string
	for _, d := range docs {
		line := "- `" + d.Name + "` (" + memoryScope(d.Repo)
		if age := MemoryAge(d.UpdatedAt, now); age != "" {
			line += ", " + age
		}
		if d.UpdatedBy.Kind == PrincipalHuman {
			line += ", written by hand"
		}
		line += ")"
		if s := strings.TrimSpace(d.Summary); s != "" {
			line += ": " + s
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// MemoryAge says how old a document is in the words a reader actually
// weighs a note by, and "" for a document whose write left no timestamp
// (an import of a row written before this column meant anything).
//
// Relative rather than a date, because the question the index has to
// answer is "should I trust this", not "when exactly was it written":
// the answer to the first is "it is four months old", and a run that
// wants the second reads the document, which says so.
//
// Exported because the same words are owed to the agent that lists or
// reads a document through its own tools, and that answer is assembled a
// package away (cmd/grain's daemonMemory, filling mcp's own entries):
// pkg/mcp deliberately does not import this package, and two spellings
// of "how old is this" -- one in the prompt and one in the tool answer
// -- would be two things to keep saying the same.
func MemoryAge(updated, now time.Time) string {
	if updated.IsZero() || now.IsZero() {
		return ""
	}
	days := int(now.Sub(updated).Hours() / 24)
	switch {
	case days < 0:
		// A clock that disagrees with itself is not worth a sentence
		// about the future; say nothing rather than "written in 3 days".
		return ""
	case days == 0:
		return "written today"
	case days == 1:
		return "written yesterday"
	case days < 60:
		return fmt.Sprintf("written %d days ago", days)
	}
	return fmt.Sprintf("written %d months ago", days/30)
}
