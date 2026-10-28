// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package mcp

import (
	"context"
	"fmt"
	"strings"
)

// The bounds update_memory holds a call to, in bytes -- the same limits
// the daemon enforces on the write (model.MaxMemoryLength,
// model.MaxMemoryDocumentLength, model.MaxMemoryDocumentSummaryLength),
// stated here so a call that is too long is refused where the agent can
// still cut it down in the same turn rather than after a round trip.
//
// Two of them are three orders of magnitude apart, and that difference
// is the whole of what memory and a memory document are: a memory is
// carried in every prompt this deployment issues afterwards, and a
// document is read by the one run that asks for it. The summary bound is
// the first kind again -- an index line is prompt, so it is bounded like
// prompt.
const (
	MaxMemoryLength                = 8 * 1024
	MaxMemoryDocumentLength        = 64 * 1024
	MaxMemoryDocumentSummaryLength = 200
)

// MemoryDocumentEntry is one line of what grain keeps in full: which
// scope a note is filed under, what it is called, what it is about, how
// old it is and how long it is -- everything but the note.
//
// Age is words rather than a timestamp ("written 12 days ago"), and is
// formatted by the side of the hop that has a clock and the row's own
// UpdatedAt (cmd/grain's daemonMemory, through model.MemoryAge), because
// the question a listing has to answer about a note nobody has looked at
// for a season is "should I trust this" and not "when exactly".
// ByHand is whether a person wrote this note rather than an agent,
// which is the other thing a run weighing a note is entitled to know.
// The run reading this listing is, more often than not, the memory agent
// deciding what to replace -- and "replace what has stopped being true"
// must not quietly mean "replace what an operator just fixed". A bool
// rather than a name because there are only two answers worth acting on
// differently, and a row whose author was never recorded (every one
// written before model.Memory.UpdatedBy existed) is false: unknown is
// not a claim that an agent wrote it, it is the absence of the mark.
type MemoryDocumentEntry struct {
	Repo    string
	Name    string
	Summary string
	Age     string
	Length  int
	ByHand  bool
}

// MemoryDocumentText is one document, read whole: the entry above plus
// the thing itself.
type MemoryDocumentText struct {
	Repo    string
	Name    string
	Summary string
	Age     string
	Text    string
	ByHand  bool
}

// MemoryStore is what grain remembers about working here, as the run
// holding the "memory" capability may touch it: the block of prose every
// later dispatch carries (WriteMemory), and the named documents no
// prompt carries but any of those runs may look up.
//
// An interface, like StatusReporter and PullRequestOpener next door, and
// for the same reason: the real implementation
// (pkg/cli/mcpserver.go's daemonMemory) asks the daemon over its REST
// API, because the rows being written live there and a run holds no
// credential of its own.
//
// Unlike those two it names a repo, because memory is the one thing a
// run writes that is not about its own task: the memory agent reads what
// a whole deployment has been doing and writes down what it noticed
// about each repo in it. A repo of "" is this deployment's own scope
// throughout, never a missing argument.
type MemoryStore interface {
	WriteMemory(ctx context.Context, repo, text string) error
	WriteMemoryDocument(ctx context.Context, repo, name, summary, text string) error
	ListMemoryDocuments(ctx context.Context) ([]MemoryDocumentEntry, error)
	ReadMemoryDocument(ctx context.Context, repo, name string) (MemoryDocumentText, error)
}

// NewMemoryTools returns the three tools a run holding the "memory"
// capability gets: update_memory, list_memory_documents and
// read_memory_document.
//
// They are what makes memory (model/memory.go, model/memory_document.go)
// a thing an agent can maintain rather than a settings box somebody has
// to keep by hand. The nightly memory agent reads this deployment's own
// state -- the tasks, the conversations, the runs, all of them files in
// the state repository -- and writes down the patterns that would have
// saved the runs it just read: which suite is flaky, which repo needs a
// build step before its tests, which review comment keeps being made.
// What it writes with no document name is added to the prompt of every
// run afterwards (orchestrator's memorySection), which is both why the
// tool exists and why its description spends as long as it does on what
// does *not* belong in one.
//
// The other two are the way out of that bound rather than around it
// (grain/task-270). A memory is 8 KiB per scope and read by every later
// run; a document is a named note nothing carries, listed in one line
// each in the same prompt, and read only by a run that decides the line
// bears on what it is doing. So the injected memory keeps saying only
// what changes what a run does, and the detail behind it has somewhere
// to live that is not a transcript nobody reads twice.
//
// Reading is on the same capability as writing, which is the arrangement
// that makes a stale document somebody's problem: every run that can
// find out a note has gone stale can replace it in the same turn.
//
// Whole-text replacement rather than an append, for the reason
// ui.memoryRequest gives: a memory that could only grow could never be
// corrected, and a wrong note repeated on every dispatch is worse than
// no memory at all. The current text is in the prompt of any run that
// can call this, so replacing it is a matter of sending back what is
// still true. A document is replaced whole for the same reason, and read
// first when it is the part that is not in the prompt.
//
// store nil returns the tools anyway, refusing every call -- what lets
// each agent framework's allowedTools enumerate the names this package
// registers without holding a live store, exactly as
// NewStatusTools and NewOpenPullRequestTools do.
func NewMemoryTools(store MemoryStore) []Tool {
	return []Tool{
		updateMemoryTool(store),
		listMemoryDocumentsTool(store),
		readMemoryDocumentTool(store),
	}
}

func updateMemoryTool(store MemoryStore) Tool {
	return Tool{
		Name: "update_memory",
		Description: "Replace what grain remembers about working here -- the notes it adds to " +
			"the prompt of every run it dispatches. Leave \"repo\" out to write the " +
			"general memory for your scope -- your team's, or the deployment's when your " +
			"grant reaches the whole deployment -- or set it to \"owner/name\" to write " +
			"the one only runs targeting that repository get. Write what would have saved the runs " +
			"you just read: a suite that fails for its own reasons rather than for the " +
			"change under it, a build step a repository needs before its tests mean " +
			"anything, a review comment that keeps being made, a path where work keeps " +
			"going wrong. Do not write what the repository already says (its README, " +
			"its CLAUDE.md, its own docs), what one task alone needed, or a narrative " +
			"of what happened last night -- a memory is read by every run forever, so " +
			"it earns its place by changing what a run would otherwise do. " +
			"\"text\" replaces the whole memory for that scope rather than adding to " +
			"it: send back what is still true, drop what is not, and send an empty " +
			"text to forget a scope entirely. What you are replacing is in your own " +
			"prompt already. Keep it under " + fmt.Sprint(MaxMemoryLength) + " characters. " +
			"Set \"document\" to write a note that is too long for that budget instead: " +
			"it is kept out of every prompt, and only its \"summary\" is listed there, " +
			"for a later run to read in full with read_memory_document when the summary " +
			"says it matters. Use one when the short memory can only gesture at " +
			"something -- why a suite fails and what to do about it, how a subsystem " +
			"actually fits together, what four runs in a row got wrong here -- and keep " +
			"writing the short memory too, since a document nobody is pointed at is a " +
			"document nobody reads. Writing the same document name again replaces it " +
			"whole, and an empty text deletes it.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"repo": map[string]any{
					"type": "string",
					"description": "The repository this memory is about, as \"owner/name\". " +
						"Omit it to write the general memory for your scope: your team's, or the " +
						"deployment-wide one when your grant reaches the whole deployment.",
				},
				"document": map[string]any{
					"type": "string",
					"description": "Name of the long note to write instead of the memory itself, " +
						"as a short slug: lower-case letters, digits and hyphens, like " +
						"\"flaky-e2e\". Omit it to write the memory carried in every prompt.",
				},
				"summary": map[string]any{
					"type": "string",
					"description": "One line saying what that document is about -- what a later " +
						"run reads in its own prompt to decide whether to open it. Required " +
						"with \"document\", ignored without it, and at most " +
						fmt.Sprint(MaxMemoryDocumentSummaryLength) + " characters, since this " +
						"is the part every prompt pays for.",
				},
				"text": map[string]any{
					"type": "string",
					"description": "The whole of what that scope should now remember, " +
						"replacing whatever it remembered before -- or, with \"document\", " +
						"the whole of that note. Empty forgets it.",
				},
			},
			"required": []string{"text"},
		},
		Handler: func(ctx context.Context, args map[string]any) Result {
			if store == nil {
				return noMemoryRoute()
			}
			repo, _ := argString(args, "repo")
			repo = strings.TrimSpace(repo)
			text, _ := argString(args, "text")
			text = strings.TrimSpace(text)
			document, _ := argString(args, "document")
			document = strings.TrimSpace(document)
			if document != "" {
				return writeMemoryDocument(ctx, store, args, repo, document, text)
			}
			if len(text) > MaxMemoryLength {
				return Result{
					Text: fmt.Sprintf("That memory is %d characters; keep it under %d. It is added "+
						"to every run's prompt, so cut it to the notes that would change what a "+
						"run does, and put the rest in a document by naming one in \"document\".",
						len(text), MaxMemoryLength),
					IsError: true,
				}
			}
			if err := store.WriteMemory(ctx, repo, text); err != nil {
				return Result{Text: err.Error(), IsError: true}
			}
			return Result{Text: memoryWritten(repo, text)}
		},
	}
}

// writeMemoryDocument is update_memory's other half: the same call with
// a document named, which writes the long note rather than the block of
// prose every prompt carries.
//
// One tool rather than two, because there is one decision behind it --
// what grain should remember about this scope -- and the only thing that
// changes is whether the answer is short enough to repeat forever. A
// separate write tool would be a second description saying most of the
// same things, and a writer that had to choose between them before it
// had written anything.
func writeMemoryDocument(ctx context.Context, store MemoryStore, args map[string]any,
	repo, document, text string) Result {
	summary, _ := argString(args, "summary")
	summary = strings.TrimSpace(summary)
	switch {
	case len(text) > MaxMemoryDocumentLength:
		return Result{
			Text: fmt.Sprintf("That document is %d characters; keep it under %d. Past that it has "+
				"stopped being a note somebody can read in a turn -- split it, or leave the rest "+
				"in the tasks and the state repository.", len(text), MaxMemoryDocumentLength),
			IsError: true,
		}
	case text != "" && summary == "":
		return Result{
			Text: "A document needs a \"summary\": the index in every run's prompt is summaries, " +
				"and a note nobody can tell the subject of is one nobody will ever open. Say in " +
				"one line what this document is about.",
			IsError: true,
		}
	case len(summary) > MaxMemoryDocumentSummaryLength:
		return Result{
			Text: fmt.Sprintf("That summary is %d characters; keep it under %d. It is the one line "+
				"about this document that every run's prompt carries, so it says what the document "+
				"is about rather than what it says.", len(summary), MaxMemoryDocumentSummaryLength),
			IsError: true,
		}
	}
	if err := store.WriteMemoryDocument(ctx, repo, document, summary, text); err != nil {
		return Result{Text: err.Error(), IsError: true}
	}
	return Result{Text: memoryDocumentWritten(repo, document, text)}
}

func listMemoryDocumentsTool(store MemoryStore) Tool {
	return Tool{
		Name: "list_memory_documents",
		ElisionAdvice: "read one of them on its own with read_memory_document, which is " +
			"where a note's own text lives",
		Description: "List the long notes grain keeps about working here -- every scope's, not " +
			"just this run's -- with what each one is about, how old it is and how long. " +
			"These are the notes that are deliberately not in any prompt: what the memory " +
			"in your own prompt carries about them is one line each, and this is the same " +
			"index for every scope, including repositories this run is not working in. Use " +
			"it to see what has already been written down before writing another one, and " +
			"to find the note that has gone stale. Read one with read_memory_document.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           map[string]any{},
		},
		Handler: func(ctx context.Context, _ map[string]any) Result {
			if store == nil {
				return noMemoryRoute()
			}
			docs, err := store.ListMemoryDocuments(ctx)
			if err != nil {
				return Result{Text: err.Error(), IsError: true}
			}
			return Result{Text: memoryDocumentList(docs)}
		},
	}
}

func readMemoryDocumentTool(store MemoryStore) Tool {
	return Tool{
		Name:          "read_memory_document",
		ElisionAdvice: elisionAdviceMemoryDocument,
		Description: "Read one of grain's long notes in full, by the name the index in your " +
			"prompt gives it (or list_memory_documents). Leave \"repo\" out for a note filed " +
			"under this deployment, or set it to \"owner/name\" for one filed under a " +
			"repository. It is worth a call whenever the one-line summary touches what you " +
			"are doing -- that summary is all the prompt could afford, and the note behind it " +
			"is what an earlier run actually worked out. If what you find does not match the " +
			"tree in front of you, the tree is right: say so in what you report, and replace " +
			"the document with update_memory, which is the same capability that let you read it.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "The document's name, as the index spells it -- \"flaky-e2e\".",
				},
				"repo": map[string]any{
					"type": "string",
					"description": "The repository it is filed under, as \"owner/name\". Omit it " +
						"for one filed under this deployment itself.",
				},
			},
			"required": []string{"name"},
		},
		Handler: func(ctx context.Context, args map[string]any) Result {
			if store == nil {
				return noMemoryRoute()
			}
			name, _ := argString(args, "name")
			name = strings.TrimSpace(name)
			if name == "" {
				return Result{
					Text:    "Name the document to read -- list_memory_documents says which exist.",
					IsError: true,
				}
			}
			repo, _ := argString(args, "repo")
			doc, err := store.ReadMemoryDocument(ctx, strings.TrimSpace(repo), name)
			if err != nil {
				return Result{Text: err.Error(), IsError: true}
			}
			return Result{Text: memoryDocumentRead(doc)}
		},
	}
}

// noMemoryRoute is what all three tools say with no daemon to ask. The
// frameworks' allowedTools enumerate these names unconditionally, so a
// run that was granted memory in a sandbox with no route back holds
// tools that can only refuse -- and the one thing they must not do is
// refuse silently.
func noMemoryRoute() Result {
	return Result{
		Text: "This run cannot reach grain's memory (no route back to the grain daemon " +
			"that dispatched it). Carry on with your work, and report what you " +
			"would have remembered instead.",
		IsError: true,
	}
}

// memoryWritten is what the tool says back: which scope it wrote, and
// whether it wrote a memory or forgot one -- so an agent that meant to
// edit the deployment's memory and typo'd a repo name can see it in the
// answer rather than in next month's prompts.
func memoryWritten(repo, text string) string {
	scope := "This deployment's memory"
	if repo != "" {
		scope = repo + "'s memory"
	}
	if text == "" {
		return scope + " is now empty: it will not be added to any prompt until something is written for it again."
	}
	return scope + " is replaced. Every run dispatched from now on is given it, until it is replaced again."
}

// memoryDocumentWritten is memoryWritten for the long form, and says the
// one thing that differs: nothing carries this text, so a later run
// reaches it only through the summary that was filed with it.
func memoryDocumentWritten(repo, document, text string) string {
	scope := "this deployment"
	if repo != "" {
		scope = repo
	}
	if text == "" {
		return "The document " + document + " for " + scope + " is deleted: it is out of the index " +
			"every prompt carries, and nothing can read it any more."
	}
	return "The document " + document + " for " + scope + " is written. No prompt carries its text; " +
		"every run dispatched for that scope from now on is given its summary in one line, and reads " +
		"the rest with read_memory_document if it matters."
}

// memoryDocumentList renders a listing for the agent that asked: one
// line per document, in the order the daemon gave them (this
// deployment's own first, then each repo, each in name order), and a
// sentence for the deployment that has written none at all -- which
// would otherwise be an empty answer indistinguishable from a failure.
func memoryDocumentList(docs []MemoryDocumentEntry) string {
	if len(docs) == 0 {
		return "grain keeps no long notes yet, for any scope. update_memory with a \"document\" " +
			"name is how the first one is written."
	}
	lines := []string{"grain's long notes, by scope. Read one with read_memory_document:", ""}
	for _, d := range docs {
		scope := "this deployment"
		if d.Repo != "" {
			scope = d.Repo
		}
		about := []string{scope}
		if d.Age != "" {
			about = append(about, d.Age)
		}
		if d.ByHand {
			about = append(about, "written by hand")
		}
		if d.Length > 0 {
			about = append(about, fmt.Sprintf("%d characters", d.Length))
		}
		line := "- " + d.Name + " (" + strings.Join(about, ", ") + ")"
		if d.Summary != "" {
			line += ": " + d.Summary
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// memoryDocumentRead is one document as the agent reading it sees it:
// its own heading, saying what it is and how old it is, and then the
// text.
//
// The age is repeated here rather than left to the index the caller read
// a moment ago, because this is the answer a run acts on -- and the one
// fact that decides how much to trust it is how long ago somebody else
// wrote it. Who wrote it is repeated with it, and for the sharper half
// of the same reason: a note a person typed is one to correct only when
// it has actually stopped being true, and this answer is what a run
// deciding to replace one is looking at.
func memoryDocumentRead(doc MemoryDocumentText) string {
	scope := "this deployment"
	if doc.Repo != "" {
		scope = doc.Repo
	}
	heading := "grain's note " + doc.Name + ", about " + scope
	if doc.Age != "" {
		heading += ", " + doc.Age
	}
	if doc.ByHand {
		heading += ", written by hand by a person rather than by an agent"
	}
	heading += "."
	if doc.Summary != "" {
		heading += " " + doc.Summary
	}
	return heading + "\n\n" + elideMiddle(doc.Text, int(maxToolResultBytes.Load()), elisionAdviceMemoryDocument)
}

// elisionAdviceMemoryDocument is the advice for a note longer than one
// answer carries. A document may be written up to
// model.MaxMemoryDocumentLength, which is four times the cap, and there
// is no narrower read of one: the tool takes a name and nothing else.
//
// So it says the two true things. The middle is not fetchable, which a
// run has to know before it writes a report as if it had read the whole
// note; and a note that long is one to split, which the reader can
// actually do -- read_memory_document is only ever registered alongside
// update_memory, so whoever is reading this holds the capability that
// rewrites it.
const elisionAdviceMemoryDocument = "there is no narrower read of a note -- if the middle " +
	"matters, say in what you report that you could not see it, and consider splitting a note " +
	"this long into several with update_memory, the same capability that let you read it"
