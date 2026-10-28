import { useState } from "react";
import {
  Box,
  Button,
  FormControl,
  InputLabel,
  MenuItem,
  Select,
  Stack,
  Typography,
} from "@mui/material";
import api from "../api.js";
import CompositionPanel from "./CompositionPanel.jsx";
import ItemGlyph, { GlyphLabel } from "./ItemGlyph.jsx";
import Overlay from "./Overlay.jsx";
import BranchField from "./BranchField.jsx";
import RepoField from "./RepoField.jsx";
import { defaultRepoChoice } from "../state.js";

// SuiteRunOverlay starts a suite running against a repo and branch
// (bwsalmon/agents#642's own "run the template against a repo and
// branch") -- opened from a suite row's own "Run" action, with that
// suite preselected but still changeable, since a suite is meant to be
// run against more than one repo/branch over its life.
export default function SuiteRunOverlay({
  suites,
  suiteId,
  repoOptions,
  config,
  onClose,
  onStarted,
  showError,
}) {
  const [selectedSuiteId, setSelectedSuiteId] = useState(
    suiteId || (suites[0] && suites[0].id) || "",
  );
  // The repo and branch the preview below is composed against. Settled
  // values, not keystrokes: RepoField's onCommit is a repo somebody has
  // picked or finished typing, and the branch field reports on blur, so
  // filling the form in does not ask the server for a composition per
  // character. The run itself still reads the form, not this state.
  //
  // The repo starts where the field visibly starts -- the deployment's
  // default target, or the first option (defaultRepoChoice) -- so the
  // preview and the branch suggestions describe the repo on screen
  // before anybody touches it.
  const options = repoOptions;
  const [initialRepo] = useState(() => defaultRepoChoice(config, options));
  const [previewRepo, setPreviewRepo] = useState(initialRepo);
  const [previewBase, setPreviewBase] = useState("");
  const previewPath = selectedSuiteId
    ? `/api/suites/${selectedSuiteId}/composition?` +
      new URLSearchParams({ repo: previewRepo, base: previewBase }).toString()
    : null;

  const submit = async (evt) => {
    evt.preventDefault();
    const data = new FormData(evt.target);
    const payload = {
      suiteId: selectedSuiteId,
      repo: data.get("repo") || "",
      base: data.get("base") || "",
    };
    try {
      await api("/api/suite-runs", {
        method: "POST",
        body: JSON.stringify(payload),
      });
      onClose();
      if (onStarted) onStarted();
    } catch (err) {
      showError(err);
    }
  };

  return (
    <Overlay onClose={onClose} label="Run a suite">
      {/* The suites figure heads this the way it heads the page the
          overlay opened over (ListPrimitives' own `icon`, ItemGlyph.jsx)
          -- 20px, ahead of the heading rather than inside it, so "Run a
          suite" is still the whole of the heading's accessible name. */}
      <Stack direction="row" alignItems="center" spacing={1} sx={{ mb: 1 }}>
        <ItemGlyph kind="suites" size={20} />
        <Typography variant="h6" component="h2" sx={{ m: 0 }}>
          Run a suite
        </Typography>
      </Stack>
      <form onSubmit={submit}>
        <FormControl fullWidth margin="normal" size="small">
          <InputLabel id="suite-run-suite-label">Suite</InputLabel>
          <Select
            labelId="suite-run-suite-label"
            label="Suite"
            value={selectedSuiteId}
            onChange={(e) => setSelectedSuiteId(e.target.value)}
            required
          >
            {suites.map((s) => (
              <MenuItem key={s.id} value={s.id}>
                <GlyphLabel kind="suites">{s.name}</GlyphLabel>
              </MenuItem>
            ))}
          </Select>
        </FormControl>
        <Box component="label" sx={{ display: "block", mt: 2, mb: 1 }}>
          <Typography
            variant="caption"
            color="text.secondary"
            sx={{ display: "block", mb: 0.5 }}
          >
            Repo <span className="hint">owner/name</span>
          </Typography>
          <RepoField
            name="repo"
            options={options}
            defaultValue={initialRepo}
            required
            onCommit={setPreviewRepo}
          />
        </Box>
        {/* The branches grain knows about on the repo above, offered
            as suggestions (BranchField.jsx): a suite is run against a
            branch that already exists far more often than against one
            being invented here. onCommit is the same reading the box
            reported on blur before -- a branch somebody has settled on,
            which is what the preview below is composed against. */}
        <BranchField
          name="base"
          label="Branch"
          required
          helperText="every task from a template not bound to a repo of its own targets this branch, and (with auto-merge) lands back on it"
          placeholder="my-feature-branch"
          margin="normal"
          size="medium"
          repo={previewRepo}
          onCommit={setPreviewBase}
        />
        {/* What "Run" is about to file, composed against the repo and
            branch above -- the suite's own two switches applied to each
            template, so a run of thirty tasks that all auto-merge says so
            before it is started rather than afterwards. */}
        <CompositionPanel path={previewPath} heading="What each pass files" />
        <Stack
          direction="row"
          justifyContent="flex-end"
          spacing={1}
          sx={{ mt: 2 }}
        >
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="contained" disabled={!selectedSuiteId}>
            Run
          </Button>
        </Stack>
      </form>
    </Overlay>
  );
}
