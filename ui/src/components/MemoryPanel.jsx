import { useCallback, useEffect, useRef, useState } from "react";
import { Box, Button, Chip, Stack, TextField, Typography } from "@mui/material";
import api from "../api.js";
import { DISCARD_PROMPT } from "./Overlay.jsx";
import RepoPicker from "./RepoPicker.jsx";
import { useTimeZone } from "../TimeZoneContext.jsx";
import { formatDateTime } from "../time.js";
import {
  MAX_MEMORY_DOCUMENT_LENGTH,
  MAX_MEMORY_DOCUMENT_SUMMARY_LENGTH,
  MAX_MEMORY_LENGTH,
  documentSize,
  isStaleMemory,
  memoryAge,
  memoryAuthor,
  memoryScopes,
  scopeLabel,
} from "../memory.js";

// What this deployment has learned about working here, and the notes it
// keeps (grain/task-333).
//
// GET/PUT /api/memory have existed since grain/task-237 and the documents
// beside them since grain/task-270, with nothing in this UI reading
// either: the only ways to see what a deployment believes were to read
// tables/memory.json in the state repository or to be a run carrying it
// in its prompt. That is a bad place for it to live, because memory is
// exactly the kind of thing that goes wrong quietly -- a note an agent
// wrote three months ago, about a flake fixed since, is still being put
// in front of every run dispatched today and nothing in grain will ever
// notice.
//
// So this pane is two things, in this order: a reading of what the runs
// are being told, and the ability to correct it. The write path is the
// same PUT the memory agent uses (ui.Client.PutMemory /
// PutMemoryDocument, behind update_memory's "memory" capability), which
// validates every bound a form here would have to -- so the fields count
// down to the limits rather than enforcing them, and a refusal comes back
// in the daemon's own words.
//
// It is a page of its own in the rail (MemoryPage.jsx, grain/task-3100):
// the deployment's layer and each repo's, which are the two a run is
// given (model.MemoryFor). It used to sit
// at the foot of a team's Models settings, beside the standing
// instructions, where nobody looking for it would think to go.
//
// It says who wrote each note as well as when, beside the age
// (model.Memory.UpdatedBy, grain/task-394). Editing here is the same PUT
// the memory agent makes, so once a write has landed the row is the only
// thing that can tell the two apart -- and it matters both ways round: a
// correction an operator made is one tonight's agent should not quietly
// replace, and a note somebody is deciding whether to trust reads very
// differently once they know a person typed it. A memory written before
// that column existed has no author, and this pane says nothing about
// one rather than guessing at either answer (memoryAuthor).
//
// `repos` is what the scope picker at the foot offers: this deployment's
// target repos, so a memory can be written for a repo no agent has
// written one for yet. Optional -- typed owner/name works with no list
// at all, the same way every other RepoPicker here does.
//
// `canWrite()` says whether the reader may save a memory. Settings →
// Memory leaves it out: the access code is owner access
// (docs/design/settings-panel.md §6.1). A scope the reader may not write is shown read-only
// rather than offering a Save the server would refuse. Optional: without
// it every scope is editable.
//
// `unsavedRef`, when given, is pointed at a function answering "does any
// box on this pane hold an edit nobody has saved?" (grain/task-2414).
// Every box here saves only on its own Save, inside a settings pane
// whose other fields save as they are left -- so the pane passed no
// isDirty to Overlay, and Escape, a tab or Back threw a half-corrected
// memory away without a word.
export default function MemoryPanel({
  showError,
  repos,
  unsavedRef,
  onDirtyChange,
  canWrite = () => true,
}) {
  // Which boxes hold an unsaved edit, by key; written by each box as it
  // changes and read only when somebody tries to leave, so a ref.
  const edits = useRef(new Map());
  const markEdited = useCallback(
    (key, edited) => {
      if (edited) edits.current.set(key, true);
      else edits.current.delete(key);
      onDirtyChange?.(edits.current.size > 0);
    },
    [onDirtyChange],
  );
  useEffect(() => () => onDirtyChange?.(false), [onDirtyChange]);
  useEffect(() => {
    if (!unsavedRef) return undefined;
    unsavedRef.current = () => edits.current.size > 0;
    return () => {
      unsavedRef.current = null;
    };
  }, [unsavedRef]);
  const [memories, setMemories] = useState(null);
  const [documents, setDocuments] = useState(null);
  // Repos an operator has asked for a row for. They have nothing stored
  // yet -- that is the point -- so nothing in either listing would put
  // them on screen, and they are held here rather than written as an
  // empty memory, since an empty memory is how a scope is *forgotten*
  // (model.Store.PutMemory deletes the row).
  const [extraRepos, setExtraRepos] = useState([]);

  const refresh = useCallback(async () => {
    try {
      // Two reads, one for each half: the memories and the index of
      // documents without their text. A document's text is fetched only
      // when one is opened, which is the same bargain the prompt makes
      // -- every run is told what is knowable, and reads only what it
      // turns out to need.
      const [loadedMemories, loadedDocuments] = await Promise.all([
        api("/api/memory"),
        api("/api/memory/documents"),
      ]);
      setMemories(Array.isArray(loadedMemories) ? loadedMemories : []);
      setDocuments(Array.isArray(loadedDocuments) ? loadedDocuments : []);
    } catch (err) {
      showError(err);
    }
  }, [showError]);

  useEffect(() => {
    refresh();
  }, [refresh]);

  if (memories === null || documents === null) return null;

  // Every repo's memory is listed, not only the ones `repos` offers: it
  // is all the deployment's (single-tenant reset, stage 4d), and a note
  // about a repo nobody configures any more is still in a run's prompt.
  const scopes = memoryScopes(memories, documents, extraRepos);
  const known = scopes.map((s) => s.repo).filter((r) => r !== "");

  return (
    <>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        What grain has learned about working here, written by the memory agent
        rather than by a person: a block of prose per scope, added to the prompt
        of every run this deployment dispatches, with a repo&apos;s own appended
        for a task that targets it. It is kept apart from the standing
        instructions (Settings → Agents) on purpose -- those are yours and
        nothing rewrites them; this is grain&apos;s own record, and
        tonight&apos;s memory agent may replace any of it. Editing here is the
        same write the agent makes, so a note that has stopped being true can be
        corrected or cleared from this pane -- and each note says which of the
        two last wrote it.
      </Typography>
      <Stack spacing={2}>
        {scopes.map((scope) => (
          <ScopeCard
            key={scope.key}
            scope={scope}
            showError={showError}
            onChanged={refresh}
            markEdited={markEdited}
            canWrite={canWrite()}
          />
        ))}
      </Stack>
      {/* Adding a scope is only ever adding a *row*: nothing is stored
          until something is typed into it and saved, so picking a repo
          here cannot itself change what any run is told. */}
      {canWrite() && (
        <Box sx={{ mt: 3 }}>
          <Typography variant="subtitle2">Another repo</Typography>
          <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
            Only the repos something has been written about appear above. Pick
            one to write the first memory for it by hand.
          </Typography>
          <RepoPicker
            ariaLabel="Add a repo to write a memory for"
            options={repos}
            picked={known}
            onPick={(picked) =>
              setExtraRepos((current) => [...current, ...picked])
            }
          />
        </Box>
      )}
    </>
  );
}

// One scope: the memory itself, and the documents filed under it.
//
// It owns the box's contents rather than the panel above owning every
// box, so typing in one scope is not a re-render of the others -- and so
// that a refresh caused by something else on the pane (a document
// deleted two scopes down) cannot throw away half-typed text.
function ScopeCard({ scope, showError, onChanged, markEdited, canWrite }) {
  const zone = useTimeZone();
  // `stored` is what the server last said this memory is; `text` is
  // what the box holds. The two together are what "Save" and "Revert"
  // are enabled by, and they are what lets the effect below adopt an
  // outside change without overwriting an edit in progress.
  const [stored, setStored] = useState(scope.text);
  const [text, setText] = useState(scope.text);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    // Another listing arrived. Adopt it only when this box holds exactly
    // what the last one said -- an operator halfway through rewriting a
    // memory would otherwise lose it to a refresh they did not ask for,
    // and would have no way of getting it back.
    if (scope.text === stored) return;
    if (text === stored) setText(scope.text);
    setStored(scope.text);
  }, [scope.text, stored, text]);

  const save = async () => {
    if (
      text.trim() === "" &&
      !confirm(
        `Forget everything grain has learned about ${scopeLabel(scope.repo).toLowerCase()}? ` +
          "Saving an empty memory deletes it rather than storing a blank one.",
      )
    )
      return;
    setBusy(true);
    try {
      await api("/api/memory", {
        method: "PUT",
        body: JSON.stringify({ repo: scope.repo, text }),
      });
      setStored(text);
      await onChanged();
    } catch (err) {
      showError(err);
    } finally {
      setBusy(false);
    }
  };

  const age = memoryAge(scope.updatedAt);
  const stale = isStaleMemory(scope.updatedAt);
  const author = memoryAuthor(scope.updatedByKind, scope.updatedBy);
  const edited = text !== stored;
  useEffect(() => {
    markEdited?.(`memory:${scope.key}`, edited);
  }, [markEdited, scope.key, edited]);
  useEffect(
    () => () => markEdited?.(`memory:${scope.key}`, false),
    [markEdited, scope.key],
  );
  // What this scope's Save and Revert are named for. Not the text box's
  // own label: a name that contains it is a second match for anything
  // finding that box by its label.
  const memoryLabel = scope.repo
    ? `${scopeLabel(scope.repo)} memory`
    : "this deployment's memory";

  return (
    <Box
      sx={{
        border: "1px solid",
        borderColor: "divider",
        borderRadius: 1,
        p: 1.5,
      }}
    >
      <Stack
        direction="row"
        spacing={1}
        alignItems="center"
        flexWrap="wrap"
        useFlexGap
      >
        <Typography variant="subtitle2">{scopeLabel(scope.repo)}</Typography>
        {/* The age, marked when it has grown into the thing memory
            actually fails at. Nothing expires a memory and nothing here
            pretends to: the chip is a prompt to a person to go and read
            it, which is the only judgement available. */}
        {age && (
          <Chip
            size="small"
            label={age}
            color={stale ? "warning" : "default"}
            variant={stale ? "filled" : "outlined"}
            title={
              scope.updatedAt
                ? `Last written ${formatDateTime(scope.updatedAt, zone)}`
                : undefined
            }
          />
        )}
        {/* And who wrote it, which the age on its own cannot say: a
            person editing here and the memory agent make the same write,
            so this is the only thing on the pane that tells somebody
            whether the note in front of them is grain's own inference or
            an operator's correction. Absent entirely for a row from
            before the author was recorded. */}
        {author && (
          <Chip
            size="small"
            label={author.label}
            variant="outlined"
            title={author.title}
          />
        )}
        {stale && (
          <Typography variant="caption" color="text.secondary">
            still in every prompt -- worth checking it is still true
          </Typography>
        )}
        {!scope.text && !age && (
          <Chip size="small" label="nothing remembered" variant="outlined" />
        )}
        {!canWrite && (
          <Chip
            size="small"
            label="read only"
            variant="outlined"
            title="Only a system admin may change the deployment's notes: they go into its runs' prompts"
          />
        )}
      </Stack>
      <TextField
        label={memoryBoxLabel(scope)}
        // The server's limit is bytes and this counts characters, which
        // differ the moment anything non-ASCII is typed; it is a guide
        // to how much room is left, and the daemon's own refusal (with
        // its own sentence) is what actually decides.
        helperText={
          canWrite
            ? `${text.length} of ${MAX_MEMORY_LENGTH} characters. Saving an empty memory forgets this scope.`
            : undefined
        }
        value={text}
        onChange={(evt) => setText(evt.target.value)}
        InputProps={{ readOnly: !canWrite }}
        multiline
        minRows={4}
        autoComplete="off"
        fullWidth
        margin="normal"
      />
      {/* Named for the scope, as each document's buttons are below:
          every scope on the pane has its own Save and Revert, and a bare
          name makes them a column of identical controls to anything
          reading the pane by name. */}
      {canWrite && (
        <Stack direction="row" spacing={1} justifyContent="flex-end">
          <Button
            type="button"
            size="small"
            disabled={!edited || busy}
            onClick={() => setText(stored)}
            aria-label={`Revert ${memoryLabel}`}
          >
            Revert
          </Button>
          <Button
            type="button"
            size="small"
            variant="contained"
            disabled={!edited || busy}
            onClick={save}
            aria-label={`Save ${memoryLabel}`}
          >
            Save
          </Button>
        </Stack>
      )}
      <Documents
        scope={scope}
        showError={showError}
        onChanged={onChanged}
        zone={zone}
        markEdited={markEdited}
        canWrite={canWrite}
      />
    </Box>
  );
}

// The documents this scope keeps: name, summary, age and size, with one
// openable in place.
//
// Listed even when there are none, unlike most of what this pane hides
// when empty: "no notes here" is what somebody who came looking for one
// needs to be told, and it is the difference between a scope with
// nothing filed and a listing that failed to load.
function Documents({
  scope,
  showError,
  onChanged,
  zone,
  markEdited,
  canWrite,
}) {
  return (
    <Box sx={{ mt: 2 }}>
      <Typography variant="subtitle2">Documents</Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
        Notes too long to carry in a prompt. Every run dispatched is told the
        name, the age and the summary of each one; the text is read only by a
        run that asks for it.
      </Typography>
      {scope.documents.length === 0 ? (
        <Typography variant="body2" color="text.secondary">
          No documents filed under this scope.
        </Typography>
      ) : (
        <Stack spacing={1}>
          {scope.documents.map((doc) => (
            <DocumentRow
              key={doc.name}
              scope={scope}
              doc={doc}
              showError={showError}
              onChanged={onChanged}
              zone={zone}
              markEdited={markEdited}
              canWrite={canWrite}
            />
          ))}
        </Stack>
      )}
    </Box>
  );
}

// One document. It fetches its own text when it is opened -- the listing
// carries every document's name and nobody's text, which is the whole
// difference between the two API calls -- and holds it while it is open.
function DocumentRow({
  scope,
  doc,
  showError,
  onChanged,
  zone,
  markEdited,
  canWrite,
}) {
  // null while closed, and {summary, text} once opened: the two are what
  // a save sends back, and both are editable, since a note that can only
  // be deleted leaves an operator who spots one wrong sentence with
  // nothing to do but throw the whole thing away.
  const [open, setOpen] = useState(null);
  // What the server last said the open document is, so Close and the
  // pane around it can tell an edit from a document merely read.
  const [loaded, setLoaded] = useState(null);
  const [busy, setBusy] = useState(false);
  // The button a request started from, while it is disabled for that
  // request. A browser drops focus from a button it disables, so Enter on
  // Open (or Save document) left a keyboard user at the top of the page;
  // once the request settles focus goes back where it was, unless
  // something else has taken it in the meantime.
  const refocus = useRef(null);
  const holdFocus = () => {
    const el = document.activeElement;
    refocus.current = el instanceof HTMLButtonElement ? el : null;
  };
  useEffect(() => {
    if (busy || !refocus.current) return;
    const el = refocus.current;
    refocus.current = null;
    const active = document.activeElement;
    if (el.isConnected && (!active || active === document.body)) el.focus();
  }, [busy]);
  const edited =
    open !== null &&
    loaded !== null &&
    (open.summary !== loaded.summary || open.text !== loaded.text);
  const editKey = `document:${scope.key}:${doc.name}`;
  useEffect(() => {
    markEdited?.(editKey, edited);
  }, [markEdited, editKey, edited]);
  useEffect(() => () => markEdited?.(editKey, false), [markEdited, editKey]);

  const age = memoryAge(doc.updatedAt);
  const stale = isStaleMemory(doc.updatedAt);
  const author = memoryAuthor(doc.updatedByKind, doc.updatedBy);

  const toggle = async () => {
    if (open) {
      if (edited && !confirm(DISCARD_PROMPT)) return;
      setOpen(null);
      setLoaded(null);
      return;
    }
    holdFocus();
    setBusy(true);
    try {
      const query = new URLSearchParams();
      if (scope.repo) query.set("repo", scope.repo);
      const full = await api(
        `/api/memory/documents/${encodeURIComponent(doc.name)}` +
          (query.size ? `?${query}` : ""),
      );
      const opened = { summary: full.summary || "", text: full.text || "" };
      setOpen(opened);
      setLoaded(opened);
    } catch (err) {
      showError(err);
    } finally {
      setBusy(false);
    }
  };

  const write = async (body, after) => {
    holdFocus();
    setBusy(true);
    try {
      await api("/api/memory/documents", {
        method: "PUT",
        body: JSON.stringify({
          repo: scope.repo,
          name: doc.name,
          ...body,
        }),
      });
      after();
      await onChanged();
    } catch (err) {
      showError(err);
    } finally {
      setBusy(false);
    }
  };

  // Deleting is the same PUT with no text (ui.Client.PutMemoryDocument),
  // which is how the agent's own update_memory deletes one -- so there
  // is one way a document ever goes away, and it is the way the write
  // path already validates.
  const docLabel = scope.repo
    ? `${doc.name} (${scopeLabel(scope.repo)})`
    : `${doc.name} (this deployment)`;

  const remove = () =>
    confirm(
      `Delete the memory document "${doc.name}"? Nothing else keeps a copy of it.`,
    ) &&
    write({ summary: "", text: "" }, () => {
      setOpen(null);
      setLoaded(null);
    });

  return (
    <Box
      sx={{
        border: "1px solid",
        borderColor: "divider",
        borderRadius: 1,
        p: 1,
      }}
    >
      <Stack
        direction="row"
        spacing={1}
        alignItems="center"
        flexWrap="wrap"
        useFlexGap
      >
        <Typography variant="body2">
          <code>{doc.name}</code>
        </Typography>
        {age && (
          <Chip
            size="small"
            label={age}
            color={stale ? "warning" : "default"}
            variant={stale ? "filled" : "outlined"}
            title={
              doc.updatedAt
                ? `Last written ${formatDateTime(doc.updatedAt, zone)}`
                : undefined
            }
          />
        )}
        {/* The same mark the memory above carries, for the same reason:
            a document a person wrote by hand is one the memory agent is
            told to leave alone unless it has stopped being true
            (model.MemoryIndex says so on its index line too). */}
        {author && (
          <Chip
            size="small"
            label={author.label}
            variant="outlined"
            title={author.title}
          />
        )}
        <Chip
          size="small"
          label={documentSize(doc.length)}
          variant="outlined"
        />
        <Box sx={{ flex: 1 }} />
        {/* Named for the document, and for its scope, rather than a bare
            "Open" and "Delete": every row on this pane has the same two
            buttons, and a document's name can repeat across scopes, so
            anything reading the pane by name -- a screen reader's
            button list, or a test -- otherwise gets a column of
            identical controls, one of them destructive. */}
        <Button
          type="button"
          size="small"
          disabled={busy}
          onClick={toggle}
          aria-expanded={Boolean(open)}
          aria-label={`${open ? "Close" : "Open"} ${docLabel}`}
        >
          {open ? "Close" : "Open"}
        </Button>
        {canWrite && (
          <Button
            type="button"
            size="small"
            color="warning"
            disabled={busy}
            onClick={remove}
            aria-label={`Delete ${docLabel}`}
          >
            Delete
          </Button>
        )}
      </Stack>
      <Typography variant="body2" color="text.secondary">
        {doc.summary}
      </Typography>
      {open && (
        <Box sx={{ mt: 1 }}>
          {/* The fields are named for the document and its scope too,
              for the buttons' reason above: two open documents otherwise
              give two boxes called "Summary", and a name repeated across
              scopes two called "Document: <name>". The visible label is
              unchanged and is the start of each name. */}
          <TextField
            label="Summary"
            inputProps={{ "aria-label": `Summary of ${docLabel}` }}
            helperText={`The one line every prompt carries about this note -- what it is about, not what it says. At most ${MAX_MEMORY_DOCUMENT_SUMMARY_LENGTH} characters.`}
            value={open.summary}
            onChange={(evt) =>
              setOpen((current) => ({ ...current, summary: evt.target.value }))
            }
            InputProps={{ readOnly: !canWrite }}
            autoComplete="off"
            fullWidth
            margin="dense"
          />
          <TextField
            label={`Document: ${doc.name}`}
            inputProps={{ "aria-label": `Document: ${docLabel}` }}
            helperText={`${open.text.length} of ${MAX_MEMORY_DOCUMENT_LENGTH} characters. No prompt carries this; a run reads it when the summary above tells it to.`}
            value={open.text}
            onChange={(evt) =>
              setOpen((current) => ({ ...current, text: evt.target.value }))
            }
            InputProps={{ readOnly: !canWrite }}
            multiline
            minRows={8}
            autoComplete="off"
            fullWidth
            margin="dense"
          />
          {canWrite && (
            <Stack direction="row" spacing={1} justifyContent="flex-end">
              <Button
                type="button"
                size="small"
                variant="contained"
                disabled={busy}
                onClick={() => {
                  // A document saved with no text is deleted
                  // (ui.Client.PutMemoryDocument trims it, and an empty one
                  // is the delete), so emptying the box and saving is the
                  // Delete button by another route -- and asks what it asks.
                  if (open.text.trim() === "") {
                    remove();
                    return;
                  }
                  const saved = { summary: open.summary, text: open.text };
                  write(saved, () => setLoaded(saved));
                }}
                aria-label={`Save document ${docLabel}`}
              >
                Save document
              </Button>
            </Stack>
          )}
        </Box>
      )}
    </Box>
  );
}

// memoryBoxLabel is a scope's text box label, which is also how a reader
// (or a test) finds the box: the deployment's and a repo's keep the
// words they have always had.
function memoryBoxLabel(scope) {
  return scope.repo
    ? `Memory about ${scope.repo}`
    : "Memory carried by every run";
}
