import { useRef, useState } from "react";
import {
  Box,
  Button,
  Checkbox,
  FormControl,
  FormControlLabel,
  InputLabel,
  ListItemText,
  MenuItem,
  Select,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import api from "../api.js";
import CompositionPanel from "./CompositionPanel.jsx";
import ItemGlyph from "./ItemGlyph.jsx";
import { DeclaredFields, DeclaredNotice } from "./Declared.jsx";
import Overlay from "./Overlay.jsx";
import { StaleEditNotice, isStaleEdit, withReload } from "./StaleEdit.jsx";
import TemplateOverlay from "./TemplateOverlay.jsx";
import { useUnsavedEdits } from "../unsavedEdits.js";
import { templateLabeler } from "../templateLabels.js";

// SuiteOverlay is the suites page's own sub page (bwsalmon/
// agents#642), TemplateOverlay's own "+ on the list opens this blank,
// clicking a row opens it pre-filled" split: a suite is a saved
// combination of templates plus how to run them (run them Count
// times, or run them until a pass produces no pull request and no
// follow-up task), created here and then run against any repo and
// branch from SuiteRunOverlay.
//
// A suite is useless with no templates, so building one from scratch
// would otherwise mean leaving this overlay for the templates page and
// back -- "+ New template" opens TemplateOverlay right on top of this
// one instead, and the template it saves is both added to the picker
// and preselected, config/repoOptions/onTemplatesChanged existing only
// to make that nested overlay work.
// The most passes a suite may ask for: model.MaxSuitePasses, which the
// server enforces on every create and update (task 3323).
export const MAX_SUITE_PASSES = 100;

function SuiteOverlay({
  suite,
  templates = [],
  repoOptions = [],
  config,
  tasks = [],
  onClose,
  onSaved,
  onTemplatesChanged,
  showError,
  onReload,
}) {
  const isNew = !suite;
  // A repository's own suite (Declared.jsx): shown, never saved or
  // deleted from here.
  const declaredIn = suite?.declaredIn || null;
  const [templateIds, setTemplateIds] = useState(
    (suite?.items || []).map((it) => it.templateId),
  );
  const [mode, setMode] = useState(suite?.mode || "until_clean");
  const offered = Array.isArray(templates) ? templates : [];
  // Names alone where they are unique, more where they are not
  // (templateLabels.js).
  const templateLabel = templateLabeler(templates);
  const templateIdsRef = useRef(templateIds);
  const modeRef = useRef(mode);
  // The rest of the form, tracked as it is typed so the preview at the
  // foot of the overlay can recompose from it -- the two switches decide
  // the auto-merge and the approval of every task a pass files, and the
  // count decides how many passes there are. The inputs stay
  // uncontrolled (defaultValue plus onChange): this state follows the
  // boxes, it does not drive them.
  const [name, setName] = useState(suite?.name || "");
  const [count, setCount] = useState(suite?.count || 1);
  const [maxPasses, setMaxPasses] = useState(suite?.maxPasses || 5);
  const [autoMerge, setAutoMerge] = useState(suite ? suite.autoMerge : true);
  const [requireApproval, setRequireApproval] = useState(
    !!suite?.requireApproval,
  );
  const [showNewTemplate, setShowNewTemplate] = useState(false);

  const templateCreated = async (tmpl) => {
    await onTemplatesChanged();
    const next = [...templateIdsRef.current, tmpl.id];
    templateIdsRef.current = next;
    setTemplateIds(next);
  };

  // suiteRequest is the CreateSuiteRequest this form describes as it
  // stands -- what submit sends, and what the preview below is composed
  // from, built in one place so the two cannot drift apart.
  const suiteRequest = () => ({
    name,
    templateIds: templateIdsRef.current,
    mode: modeRef.current,
    count: modeRef.current === "count" ? Number(count) : undefined,
    maxPasses:
      modeRef.current === "until_clean" ? Number(maxPasses) : undefined,
    requireApproval,
    autoMerge,
  });

  // Whether leaving now would lose an edit -- suiteRequest is the whole
  // of what Save sends, and the form adds the boxes it has not caught up
  // with yet.
  const { formRef, isDirty } = useUnsavedEdits(suiteRequest());
  const [stale, setStale] = useState(null);
  // Held from when the pane opened, ScheduleOverlay's openedRevision.
  const [openedRevision] = useState(suite?.revision);

  const submit = async (evt) => {
    evt.preventDefault();
    const payload = suiteRequest();
    try {
      if (isNew) {
        await api("/api/suites", {
          method: "POST",
          body: JSON.stringify(payload),
        });
      } else {
        // ScheduleOverlay's own reason for the revision.
        await api(`/api/suites/${suite.id}`, {
          method: "PATCH",
          body: JSON.stringify({ ...payload, revision: openedRevision }),
        });
      }
      await onSaved();
      onClose();
    } catch (err) {
      if (isStaleEdit(err)) setStale(err);
      else showError(err);
    }
  };

  const remove = async () => {
    if (
      !confirm(
        `Delete the suite "${suite.name}"? Runs already started from it are unaffected.`,
      )
    )
      return;
    try {
      await api(`/api/suites/${suite.id}`, { method: "DELETE" });
      await onSaved();
      onClose();
    } catch (err) {
      showError(err);
    }
  };

  return (
    <Overlay
      onClose={onClose}
      pane
      label={isNew ? "New suite" : declaredIn ? "Suite" : "Edit suite"}
      backLabel="Suites"
      isDirty={declaredIn ? null : isDirty}
    >
      {/* The same four lobes that head the Suites page and "Run a
          suite" (SuiteRunOverlay.jsx, ItemGlyph.jsx), so opening a suite
          from the list is visibly the same thing as the row that opened
          it. */}
      <Stack direction="row" alignItems="center" spacing={1} sx={{ mb: 1 }}>
        <ItemGlyph kind="suites" size={20} />
        <Typography variant="h6" component="h2" sx={{ m: 0 }}>
          {isNew ? "New suite" : declaredIn ? "Suite" : "Edit suite"}
        </Typography>
      </Stack>
      <DeclaredNotice
        declaredIn={declaredIn}
        noun="suite"
        also="It can still be run from the suites list."
      />
      <StaleEditNotice error={stale} onReload={onReload} />
      <form className="pane-form" onSubmit={submit} ref={formRef}>
        <DeclaredFields declaredIn={declaredIn}>
          <TextField
            name="name"
            label="Name"
            defaultValue={suite?.name}
            onChange={(e) => setName(e.target.value)}
            required
            InputLabelProps={{ required: false }}
            autoComplete="off"
            fullWidth
            margin="normal"
          />

          <FormControl fullWidth margin="normal" size="small">
            <InputLabel id="suite-templates-label">Templates</InputLabel>
            <Select
              labelId="suite-templates-label"
              label="Templates"
              multiple
              value={templateIds}
              onChange={(e) => {
                templateIdsRef.current = e.target.value;
                setTemplateIds(e.target.value);
              }}
              renderValue={(selected) =>
                selected
                  .map((id) => {
                    const t = templates.find((x) => x.id === id);
                    return t ? templateLabel(t) : id;
                  })
                  .join(", ")
              }
            >
              {offered.map((t) => (
                <MenuItem key={t.id} value={t.id}>
                  <Checkbox checked={templateIds.includes(t.id)} size="small" />
                  {/* The templates figure between the checkbox and the
                    name (ItemGlyph.jsx), as in every other menu that
                    picks a template -- a schedule's, a task's review.
                    After the checkbox rather than before it: the box is
                    a control, and the glyph belongs in front of the word
                    it marks. */}
                  <ItemGlyph kind="templates" />
                  {/* A bound template runs against its own repo wherever
                    this suite is run (grain/task-285), so the picker
                    says which rather than leaving that to a click. */}
                  <ListItemText
                    primary={templateLabel(t)}
                    secondary={[t.title, t.repo || null]
                      .filter(Boolean)
                      .join(" -- ")}
                    sx={{ ml: 0.9 }}
                  />
                </MenuItem>
              ))}
            </Select>
          </FormControl>
          <Button
            size="small"
            onClick={() => setShowNewTemplate(true)}
            sx={{ mt: -1, mb: 1 }}
          >
            + New template
          </Button>

          <FormControl fullWidth margin="normal" size="small">
            <InputLabel id="suite-mode-label">Run mode</InputLabel>
            <Select
              labelId="suite-mode-label"
              label="Run mode"
              value={mode}
              onChange={(e) => {
                modeRef.current = e.target.value;
                setMode(e.target.value);
              }}
            >
              <MenuItem value="until_clean">
                Run until no repo changes or new tasks
              </MenuItem>
              <MenuItem value="count">Run a fixed number of times</MenuItem>
            </Select>
          </FormControl>

          {/* Keyed apart, and each opening on what the form holds for
              it: without the keys React reuses one <input> for both
              boxes, so switching mode left the other box's number on
              show while the form sent its own. */}
          {mode === "count" ? (
            <TextField
              key="count"
              name="count"
              label="Number of times"
              type="number"
              defaultValue={count}
              onChange={(e) => setCount(e.target.value)}
              inputProps={{ min: 1, max: MAX_SUITE_PASSES }}
              required
              InputLabelProps={{ required: false }}
              margin="normal"
              sx={{ maxWidth: 220 }}
            />
          ) : (
            <TextField
              key="maxPasses"
              name="maxPasses"
              label="Max passes"
              type="number"
              defaultValue={maxPasses}
              onChange={(e) => setMaxPasses(e.target.value)}
              helperText="stops and reports a failure if no pass ever comes back clean"
              inputProps={{ min: 1, max: MAX_SUITE_PASSES }}
              required
              InputLabelProps={{ required: false }}
              margin="normal"
              sx={{ maxWidth: 220 }}
            />
          )}

          <FormControlLabel
            control={
              <Checkbox
                name="autoMerge"
                defaultChecked={suite ? suite.autoMerge : true}
                onChange={(e) => setAutoMerge(e.target.checked)}
              />
            }
            label="Auto-merge each pull request once checks pass"
            sx={{ display: "flex", mt: 1 }}
          />
          <FormControlLabel
            control={
              <Checkbox
                name="requireApproval"
                defaultChecked={suite?.requireApproval}
                onChange={(e) => setRequireApproval(e.target.checked)}
              />
            }
            label="Require approval before each pass's tasks run"
            sx={{ display: "flex" }}
          />

          <Box sx={{ mt: 1 }}>
            <Typography variant="caption" color="text.secondary">
              By default a suite's tasks auto-queue and auto-merge -- check
              "require approval" to review them first.
            </Typography>
          </Box>
        </DeclaredFields>
        <Stack
          direction="row"
          justifyContent={isNew ? "flex-end" : "space-between"}
          alignItems="center"
          sx={{ mt: 2 }}
        >
          {!isNew && (
            <Button color="error" onClick={remove} disabled={!!declaredIn}>
              Delete
            </Button>
          )}
          <Stack direction="row" spacing={1}>
            <Button onClick={onClose}>{declaredIn ? "Close" : "Cancel"}</Button>
            <Button type="submit" variant="contained" disabled={!!declaredIn}>
              {isNew ? "Add suite" : "Save"}
            </Button>
          </Stack>
        </Stack>
      </form>
      {/* Composed against no repo, since a suite has none until it is
          run: every task's placement reads "the repo this run targets"
          unless its own template is bound to one, and everything that is
          not placement -- content, capabilities, and the two switches
          above -- is already decided here.

          Composed from the form rather than from the row, so it follows
          the templates being picked and a suite that does not exist yet
          has a preview too: the body above goes to the draft endpoint,
          which resolves those templates out of the store on every call
          and writes nothing. */}
      <CompositionPanel
        path="/api/suites/composition"
        body={suiteRequest()}
        heading="What each pass files"
        note={isNew ? "as filled in above" : "as edited above, not as saved"}
      />
      {showNewTemplate && (
        <TemplateOverlay
          repoOptions={repoOptions}
          config={config}
          tasks={tasks}
          onClose={() => setShowNewTemplate(false)}
          onSaved={templateCreated}
          showError={showError}
        />
      )}
    </Overlay>
  );
}

// Reloadable (StaleEdit.jsx), so a save refused as stale can start over
// from the saved copy.
export default withReload(SuiteOverlay);
