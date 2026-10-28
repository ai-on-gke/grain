import { repoKey } from "./state.js";

// What the Settings pane makes of GET /api/memory and
// GET /api/memory/documents (grain/task-333).
//
// Memory is what this deployment has learned about working here
// (model.Memory): a block of prose per scope -- one for the deployment,
// one per repo -- appended to the prompt of every run dispatched, and
// written by the memory agent rather than by a person. Documents are the
// long form of the same thing (model.MemoryDocument): named notes no
// prompt carries, listed in every prompt by name and summary and read by
// the run that decides one matters.
//
// Both had an API and no screen until this file existed, so the only way
// to see what a deployment believes about working here was to read
// tables/memory.json in the state repository -- or to be a run that
// carried it in its prompt and could not say so afterwards.
//
// The shaping lives here rather than in MemoryPanel.jsx for the reason
// state.js exists: what a scope *is* (two listings unioned, in a fixed
// order) and how old a note reads as are decisions worth testing without
// rendering anything.

// The limits the server enforces on a write (model.MaxMemoryLength and
// friends), repeated here so a field can count down to them while it is
// being typed rather than only refusing on submit. They are a copy, and
// the server's are the ones that decide: a form that let 9 KiB through
// gets the daemon's own sentence back in the error banner, which is the
// same answer the memory agent gets from update_memory.
export const MAX_MEMORY_LENGTH = 8 * 1024;
export const MAX_MEMORY_DOCUMENT_LENGTH = 64 * 1024;
export const MAX_MEMORY_DOCUMENT_SUMMARY_LENGTH = 200;

// STALE_AFTER_DAYS is when this pane stops reporting an age and starts
// calling it stale.
//
// Staleness is the failure mode memory has and the one nothing in grain
// notices: a note written three months ago about a flake that was fixed
// two months ago is still in the prompt of every run dispatched today,
// and no test fails because of it. Nothing here expires anything -- the
// mark is the whole intervention, because the judgement is a person's.
//
// 60 days because that is where model.MemoryAge stops counting days and
// starts saying months: the wording grain itself uses in every prompt
// already treats that as the boundary between "recent" and "a while
// ago", and a second threshold with a different number would be this
// pane disagreeing with the index it is showing.
export const STALE_AFTER_DAYS = 60;

const DAY_MS = 24 * 60 * 60 * 1000;

// memoryAgeDays is how many whole days ago an ISO timestamp was, or null
// when there is no usable timestamp -- a row imported from before the
// column meant anything, or a clock that disagrees with itself badly
// enough to put the write in the future.
export function memoryAgeDays(iso, now = Date.now()) {
  if (!iso) return null;
  const at = new Date(iso).getTime();
  if (Number.isNaN(at)) return null;
  const days = Math.floor((now - at) / DAY_MS);
  return days < 0 ? null : days;
}

// memoryAge is how old a note is, in the words the prompt itself uses.
//
// Deliberately model.MemoryAge's wording, down to "written yesterday":
// the index every run is given says exactly this about the same document,
// and an operator reading the pane should be reading what the agents
// read. "" when nothing can be said, which callers render as nothing
// rather than as a made-up age.
export function memoryAge(iso, now = Date.now()) {
  const days = memoryAgeDays(iso, now);
  if (days === null) return "";
  if (days === 0) return "written today";
  if (days === 1) return "written yesterday";
  if (days < STALE_AFTER_DAYS) return `written ${days} days ago`;
  return `written ${Math.floor(days / 30)} months ago`;
}

// isStaleMemory is whether that age deserves the warning colour. A note
// with no timestamp is not called stale: unknown and old are different
// things, and the second is a claim this pane cannot make.
export function isStaleMemory(iso, now = Date.now()) {
  const days = memoryAgeDays(iso, now);
  return days !== null && days >= STALE_AFTER_DAYS;
}

// memoryAuthor is who wrote a note, in the words this pane says it in --
// or null when nothing is known about the author, which callers render
// as nothing at all.
//
// Three answers, not two. A person editing here and the memory agent
// take the same route (the same PUT, on the same daemon), which is why
// the author is a stored column at all (model.Memory.UpdatedBy): without
// it an operator's own correction and last night's agent run are the
// same row. But every memory written before that column existed has no
// author, and unknown is not a third kind of author to be dressed up as
// one of the two -- guessing "the memory agent" would be right most of
// the time and wrong exactly where this matters, on a note somebody
// fixed by hand.
//
// The words are chosen for what a reader is deciding. "Edited here" is
// where this pane is; "written by the memory agent" is what the pane's
// own introduction says the rest of it is. `id` goes in the tooltip
// rather than the chip: which operator, or which run, is the second
// question, and the first one has to fit beside an age.
export function memoryAuthor(kind, id) {
  if (kind === "human") {
    return { label: "edited here", title: id ? `Edited by ${id}` : undefined };
  }
  if (kind === "agent" || kind === "automation") {
    return {
      label: "written by the memory agent",
      title: id ? `Written by ${id}` : undefined,
    };
  }
  return null;
}

// scopeLabel names a scope the way the API keys it: repo "" is the
// deployment-wide memory every dispatch carries, and a repo is that
// repo's own note. Said in words rather than left blank, since a blank
// heading reads as a missing repo rather than as the deployment itself
// -- the same choice ui.memoryScopeName makes on the server.
export function scopeLabel(repo) {
  return repo ? repo : "This deployment";
}

// memoryScopes unions the two listings into one row per scope: the
// deployment's own first, then each repo in name order.
//
// A union rather than a walk of either one, because the two are written
// independently. A scope can have documents and no memory (an agent that
// filed a note without shortening it into the prompt), or a memory and
// no documents, and a pane built from `memories` alone would hide the
// second kind of note entirely -- which is the gap this pane exists to
// close.
//
// The deployment's row is always present, even with nothing in it: it is
// the scope every single run is given, so "nothing has been learned yet"
// is an answer somebody came here for, and it is the row an operator
// types the first memory into.
//
// Repos are matched case-insensitively, through state.js's own repoKey,
// for repoRows' reason: GitHub resolves owner/name without regard to
// case and the two listings are keyed by whatever spelling wrote them,
// so an exact match would show one repository as two rows with its
// memory in one and its documents in the other.
//
// When allowedRepos is provided, only the deployment's scope and repos
// in allowedRepos are included.
//
// Memory has no team scope (single-tenant reset, stage 4d): the
// deployment's layer and each repo's are the two a run is given
// (model.MemoryFor).
export function memoryScopes(
  memories,
  documents,
  extraRepos = [],
  allowedRepos = null,
) {
  const allowed =
    allowedRepos != null && Array.isArray(allowedRepos)
      ? new Set(allowedRepos.map(repoKey))
      : null;
  const isAllowed = (entry) => {
    const repo = entry?.repo;
    if (!repo) return true;
    if (!allowed) return true;
    return allowed.has(repoKey(repo));
  };
  const rows = new Map();
  const row = (repo) => {
    const key = repoKey(repo || "");
    if (!rows.has(key)) {
      rows.set(key, { key, repo: repo || "", text: "", documents: [] });
    }
    return rows.get(key);
  };
  row("");
  for (const repo of Array.isArray(extraRepos) ? extraRepos : []) {
    if (isAllowed({ repo })) row(repo);
  }
  for (const m of Array.isArray(memories) ? memories : []) {
    if (!isAllowed(m)) continue;
    const r = row(m?.repo || "");
    r.text = m?.text || "";
    r.updatedAt = m?.updatedAt;
    // Who wrote it travels with it (memoryAuthor): a scope row is what
    // the card is drawn from, and "an agent wrote this" is as much a
    // fact about the note as its age is.
    r.updatedBy = m?.updatedBy;
    r.updatedByKind = m?.updatedByKind;
  }
  for (const d of Array.isArray(documents) ? documents : []) {
    if (d && isAllowed(d)) row(d.repo || "").documents.push(d);
  }
  return [...rows.values()].sort((a, b) => {
    // The deployment first, whatever it is called: it is the layer every
    // run gets, and a repo's own is read as an addition to it
    // (model.MemoryFor joins them in this order too).
    if ((a.repo === "") !== (b.repo === "")) return a.repo === "" ? -1 : 1;
    return repoKey(a.repo).localeCompare(repoKey(b.repo));
  });
}

// documentSize renders a document's Length for the line that says
// whether opening it is a paragraph or a chapter. Bytes under a
// kilobyte, since a 200-byte note and a 900-byte one are both "short"
// and rounding them both to "1 KB" would say less than the number does.
export function documentSize(bytes) {
  const n = Number(bytes) || 0;
  if (n < 1024) return `${n} B`;
  return `${(n / 1024).toFixed(1)} KB`;
}
