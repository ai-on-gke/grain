import { Box } from "@mui/material";

// UnbackedStateDot is the rail's marker for a configuration reset notice
// on the Config repo pane: an 8px warning dot on the Settings nav entry
// and tab, with the full notice behind it. The historic name remains to
// avoid churn in callers; config-repository mirroring and its unbacked
// warning no longer exist.
export default function UnbackedStateDot({ sx, label, title }) {
  return (
    <Box
      component="span"
      role="img"
      aria-label={label || DOT_TEXT.label}
      title={title || DOT_TEXT.title}
      sx={{
        width: 8,
        height: 8,
        borderRadius: "50%",
        flex: "none",
        bgcolor: "warning.main",
        ...sx,
      }}
    />
  );
}

const DOT_TEXT = {
  label: "A merged change reset settings on this deployment",
  title:
    "A merged settings change replaced a value here, and grain had nowhere to say so but this " +
    "host's own journal. See Settings → Config repo.",
};

// stateDot turns GET /api/config's reset marker into dot props. A view
// rendered before the first poll answers draws no dot rather than failing.
export function stateDot(config) {
  return config?.stateRepoResetNotice ? DOT_TEXT : null;
}
