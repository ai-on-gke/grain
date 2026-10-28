import { Box, Chip, Stack } from "@mui/material";
import RepoPicker from "./RepoPicker.jsx";

// ReadOnlyReposField is the read-only repos picker (grain/task-241): the
// repos a task's run may clone but never push to (model.Task.Reads),
// picked the way its dependencies are rather than typed into a bare
// "owner/name, comma-separated" box that spelled a repo wrong as easily
// as right. RepoPicker's box, with one chip per picked repo above it.
//
// It does not offer the repo the task is filed against (grain/task-271),
// for the reason WriteReposField does not either. That repo is already
// cloned into the sandbox and is the one place the run may push, so
// there is nothing a read-only entry for it could mean: the prompt
// leaves it out of the "you may also read these" sentence
// (orchestrator.readOnlyNames) precisely because saying both about one
// repo is a contradiction, and the server now refuses the combination
// outright where somebody typed both halves in one form
// (ui.refuseTargetAmongReads). Offering it would be offering the one
// suggestion submit is guaranteed to reject.
//
// target is the repo as the form has it -- "" for a form that has no
// target to speak of (an unbound template) or none picked yet. Only the
// suggestions are filtered: a repo already picked keeps its chip, so a
// task that arrived carrying this combination from somewhere the API
// does not police (a template's Reads, a backport carrying the
// original's) still shows what it has and can still have it taken off.
//
// Selection lives in the caller's state (value/onChange, a plain array
// of owner/name strings), not in here, because all three forms that use
// this already submit as JSON built from state rather than from the DOM
// -- the same way each of them handles its capabilities picker.
export default function ReadOnlyReposField({
  options = [],
  value = [],
  onChange,
  target = "",
}) {
  const remove = (repo) => onChange(value.filter((r) => r !== repo));
  const offered = options.filter(
    (r) => r.toLowerCase() !== target.trim().toLowerCase(),
  );

  return (
    <Box sx={{ mt: 2, mb: 1 }}>
      {value.length > 0 && (
        <Stack
          direction="row"
          flexWrap="wrap"
          useFlexGap
          gap={0.5}
          sx={{ mb: 1 }}
        >
          {value.map((r) => (
            <Chip
              key={r}
              size="small"
              label={r}
              onDelete={() => remove(r)}
              deleteIcon={<span title={`Remove ${r}`}>×</span>}
            />
          ))}
        </Stack>
      )}
      <RepoPicker
        label="Read-only repos"
        helperText="optional -- cloned alongside the target repo, never pushed to"
        options={offered}
        picked={value}
        onPick={(repos) => onChange([...value, ...repos])}
      />
    </Box>
  );
}
