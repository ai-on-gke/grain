import { useEffect, useState } from "react";
import CloseIcon from "@mui/icons-material/Close";
import OpenInFullIcon from "@mui/icons-material/OpenInFull";
import ZoomInIcon from "@mui/icons-material/ZoomIn";
import ZoomOutIcon from "@mui/icons-material/ZoomOut";
import {
  Box,
  Button,
  Dialog,
  IconButton,
  Stack,
  ToggleButton,
  ToggleButtonGroup,
  Typography,
  useTheme,
} from "@mui/material";
import { getCachedMermaidSvg, renderMermaidSvg } from "../mermaidRender.js";
import { useIsPhone } from "../phone.js";

const ZOOM_STEPS = [50, 75, 100, 125, 150, 200, 250];

function useRenderedMermaid(code, mode) {
  const trimmed = String(code ?? "").trim();
  const [state, setState] = useState(() => {
    const cached = trimmed ? getCachedMermaidSvg(mode, trimmed) : null;
    return {
      key: `${mode}\0${trimmed}`,
      svg: cached,
      error: trimmed ? null : "Empty Mermaid diagram",
    };
  });

  useEffect(() => {
    const currentKey = `${mode}\0${trimmed}`;
    if (!trimmed) {
      setState({
        key: currentKey,
        svg: null,
        error: "Empty Mermaid diagram",
      });
      return undefined;
    }
    const cached = getCachedMermaidSvg(mode, trimmed);
    if (cached) {
      setState({ key: currentKey, svg: cached, error: null });
      return undefined;
    }

    let live = true;
    setState((prev) =>
      prev.key === currentKey
        ? prev
        : { key: currentKey, svg: null, error: null },
    );
    renderMermaidSvg(trimmed, mode).then(
      (svg) => {
        if (live) {
          setState({ key: currentKey, svg, error: null });
        }
      },
      (err) => {
        if (live) {
          setState({
            key: currentKey,
            svg: null,
            error: err?.message || "Invalid Mermaid syntax",
          });
        }
      },
    );
    return () => {
      live = false;
    };
  }, [trimmed, mode]);

  const currentKey = `${mode}\0${trimmed}`;
  if (state.key !== currentKey) {
    const cached = trimmed ? getCachedMermaidSvg(mode, trimmed) : null;
    return {
      svg: cached,
      error: trimmed ? null : "Empty Mermaid diagram",
      loading: Boolean(trimmed && !cached),
    };
  }
  return {
    svg: state.svg,
    error: state.error,
    loading: !state.svg && !state.error,
  };
}

function MermaidSource({ code, label }) {
  return (
    <pre className="mermaid-source" aria-label={label}>
      <code className="language-mermaid">{code}</code>
    </pre>
  );
}

function DiagramCanvas({ code, mode, zoom = 100, label }) {
  const { svg, error, loading } = useRenderedMermaid(code, mode);

  if (error) {
    return (
      <div className="mermaid-fallback">
        <div className="mermaid-fallback-note" role="status">
          Diagram could not be rendered ({error}) — showing Mermaid source.
        </div>
        <MermaidSource code={code} label={label} />
      </div>
    );
  }

  if (loading) {
    return (
      <div className="mermaid-loading" role="status">
        Rendering diagram…
      </div>
    );
  }

  return (
    <div className="mermaid-canvas" aria-label={label}>
      <div
        className="mermaid-svg-wrap"
        style={
          zoom !== 100
            ? {
                transform: `scale(${zoom / 100})`,
                transformOrigin: "top center",
              }
            : undefined
        }
        // Safe: `svg` has just passed through Mermaid's strict mode AND
        // `sanitizeMermaidSvg`, which strips script/iframe/object/form
        // elements, all `on*` attributes, and any URL rejected by `safeUrl`.
        dangerouslySetInnerHTML={{ __html: svg }}
      />
    </div>
  );
}

function ZoomControls({ zoom, setZoom }) {
  const index = ZOOM_STEPS.indexOf(zoom);
  const canOut = index > 0;
  const canIn = index !== -1 && index < ZOOM_STEPS.length - 1;
  return (
    <Stack direction="row" spacing={0.5} alignItems="center">
      <IconButton
        size="small"
        aria-label="Zoom out"
        disabled={!canOut}
        onClick={() => canOut && setZoom(ZOOM_STEPS[index - 1])}
      >
        <ZoomOutIcon fontSize="small" />
      </IconButton>
      <Button
        size="small"
        variant="text"
        aria-label="Reset zoom"
        onClick={() => setZoom(100)}
        sx={{ minWidth: "3.2rem", px: 0.5 }}
      >
        {zoom}%
      </Button>
      <IconButton
        size="small"
        aria-label="Zoom in"
        disabled={!canIn}
        onClick={() => canIn && setZoom(ZOOM_STEPS[index + 1])}
      >
        <ZoomInIcon fontSize="small" />
      </IconButton>
    </Stack>
  );
}

// Full-screen viewer modelled on AttachmentLinks's ImageView so iOS
// home-screen installs and narrow task panes can inspect wide architecture
// diagrams without navigating away from the open task.
function DiagramDialog({
  open,
  onClose,
  title,
  mode,
  code,
  comparison = null,
}) {
  const [zoom, setZoom] = useState(100);
  const [dialogView, setDialogView] = useState("split");

  useEffect(() => {
    if (!open || !comparison) return undefined;
    const onKeyDown = (event) => {
      if (event.key === "ArrowLeft") {
        setDialogView("before");
      } else if (event.key === "ArrowRight") {
        setDialogView("after");
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [open, comparison]);

  if (!open) return null;

  return (
    <Dialog
      open
      onClose={onClose}
      fullScreen
      aria-label={title || "Expanded diagram"}
      PaperProps={{
        sx: {
          bgcolor: "background.default",
          backgroundImage: "none",
          display: "flex",
          flexDirection: "column",
        },
      }}
    >
      <Box
        sx={{
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
          flexWrap: "wrap",
          gap: 1,
          px: 2,
          py: 1,
          pt: "calc(8px + env(safe-area-inset-top, 0px))",
          borderBottom: "1px solid var(--border)",
          bgcolor: "background.paper",
        }}
      >
        <Typography variant="subtitle2" sx={{ fontWeight: 600 }}>
          {title || (comparison ? "before & after" : "diagram")}
        </Typography>
        <Stack direction="row" spacing={1} alignItems="center" flexWrap="wrap">
          {comparison && (
            <ToggleButtonGroup
              size="small"
              exclusive
              value={dialogView}
              aria-label="Expanded comparison layout"
              onChange={(event, next) => next && setDialogView(next)}
            >
              <ToggleButton value="split">Side by side</ToggleButton>
              <ToggleButton value="before">Before</ToggleButton>
              <ToggleButton value="after">After</ToggleButton>
              <ToggleButton value="stacked">Stacked</ToggleButton>
            </ToggleButtonGroup>
          )}
          <ZoomControls zoom={zoom} setZoom={setZoom} />
          <IconButton aria-label="Close diagram" onClick={onClose} size="small">
            <CloseIcon fontSize="small" />
          </IconButton>
        </Stack>
      </Box>

      <Box
        sx={{
          flex: 1,
          overflow: "auto",
          p: 2,
          pb: "calc(16px + env(safe-area-inset-bottom, 0px))",
        }}
      >
        {comparison ? (
          <ComparisonPanels
            viewType={dialogView}
            beforeCode={comparison.beforeCode}
            afterCode={comparison.afterCode}
            beforeLabel={comparison.beforeLabel}
            afterLabel={comparison.afterLabel}
            mode={mode}
            zoom={zoom}
          />
        ) : (
          <DiagramCanvas
            code={code}
            mode={mode}
            zoom={zoom}
            label={title || "Expanded diagram"}
          />
        )}
      </Box>
    </Dialog>
  );
}

function ComparisonPanels({
  viewType,
  beforeCode,
  afterCode,
  beforeLabel,
  afterLabel,
  mode,
  zoom = 100,
}) {
  if (viewType === "source") {
    return (
      <div className="mermaid-comparison-grid mermaid-comparison-split">
        <div className="mermaid-pane">
          <div className="mermaid-pane-header">
            <span className="mermaid-state-tag mermaid-state-before">
              {beforeLabel}
            </span>
          </div>
          <MermaidSource code={beforeCode} label={`${beforeLabel} source`} />
        </div>
        <div className="mermaid-pane">
          <div className="mermaid-pane-header">
            <span className="mermaid-state-tag mermaid-state-after">
              {afterLabel}
            </span>
          </div>
          <MermaidSource code={afterCode} label={`${afterLabel} source`} />
        </div>
      </div>
    );
  }

  if (viewType === "before") {
    return (
      <div className="mermaid-pane">
        <div className="mermaid-pane-header">
          <span className="mermaid-state-tag mermaid-state-before">
            {beforeLabel}
          </span>
        </div>
        <DiagramCanvas
          code={beforeCode}
          mode={mode}
          zoom={zoom}
          label={beforeLabel}
        />
      </div>
    );
  }

  if (viewType === "after") {
    return (
      <div className="mermaid-pane">
        <div className="mermaid-pane-header">
          <span className="mermaid-state-tag mermaid-state-after">
            {afterLabel}
          </span>
        </div>
        <DiagramCanvas
          code={afterCode}
          mode={mode}
          zoom={zoom}
          label={afterLabel}
        />
      </div>
    );
  }

  const gridClass =
    viewType === "stacked"
      ? "mermaid-comparison-grid mermaid-comparison-stacked"
      : "mermaid-comparison-grid mermaid-comparison-split";

  return (
    <div className={gridClass}>
      <div className="mermaid-pane">
        <div className="mermaid-pane-header">
          <span className="mermaid-state-tag mermaid-state-before">
            {beforeLabel}
          </span>
        </div>
        <DiagramCanvas
          code={beforeCode}
          mode={mode}
          zoom={zoom}
          label={beforeLabel}
        />
      </div>
      <div className="mermaid-pane">
        <div className="mermaid-pane-header">
          <span className="mermaid-state-tag mermaid-state-after">
            {afterLabel}
          </span>
        </div>
        <DiagramCanvas
          code={afterCode}
          mode={mode}
          zoom={zoom}
          label={afterLabel}
        />
      </div>
    </div>
  );
}

export function MermaidDiagram({ code = "", title = "" }) {
  const theme = useTheme();
  const mode = theme.palette.mode === "dark" ? "dark" : "light";
  const [view, setView] = useState("diagram");
  const [expanded, setExpanded] = useState(false);

  return (
    <div
      className="mermaid-block"
      role="region"
      aria-label={title || "Mermaid diagram"}
    >
      <div className="mermaid-toolbar">
        <span className="mermaid-title">{title || "diagram"}</span>
        <Stack direction="row" spacing={0.75} alignItems="center">
          <ToggleButtonGroup
            size="small"
            exclusive
            value={view}
            aria-label="Diagram view"
            onChange={(event, next) => next && setView(next)}
          >
            <ToggleButton value="diagram">Diagram</ToggleButton>
            <ToggleButton value="source">Source</ToggleButton>
          </ToggleButtonGroup>
          <IconButton
            size="small"
            aria-label="Expand diagram"
            title="Expand diagram"
            onClick={() => setExpanded(true)}
          >
            <OpenInFullIcon sx={{ fontSize: 15 }} />
          </IconButton>
        </Stack>
      </div>

      {view === "source" ? (
        <MermaidSource code={code} label={`${title || "Diagram"} source`} />
      ) : (
        <DiagramCanvas code={code} mode={mode} label={title || "Diagram"} />
      )}

      <DiagramDialog
        open={expanded}
        onClose={() => setExpanded(false)}
        title={title || "diagram"}
        mode={mode}
        code={code}
      />
    </div>
  );
}

export function MermaidComparison({
  beforeCode = "",
  afterCode = "",
  beforeLabel = "Before",
  afterLabel = "After",
}) {
  const theme = useTheme();
  const mode = theme.palette.mode === "dark" ? "dark" : "light";
  const isPhone = useIsPhone();
  // Kept null until a layout is chosen explicitly so rotating a phone or
  // dragging a window narrow adapts automatically (same contract as
  // DiffView.jsx).
  const [chosenView, setChosenView] = useState(null);
  const [expanded, setExpanded] = useState(false);
  const viewType = chosenView || (isPhone ? "before" : "split");

  return (
    <div
      className="mermaid-block mermaid-comparison"
      role="region"
      aria-label="Before and after architecture diagrams"
    >
      <div className="mermaid-toolbar">
        <span className="mermaid-title">before &amp; after</span>
        <Stack
          direction="row"
          spacing={0.75}
          alignItems="center"
          flexWrap="wrap"
        >
          <ToggleButtonGroup
            size="small"
            exclusive
            value={viewType}
            aria-label="Diagram comparison layout"
            onChange={(event, next) => next && setChosenView(next)}
          >
            <ToggleButton value="split">Side by side</ToggleButton>
            <ToggleButton value="before">Before</ToggleButton>
            <ToggleButton value="after">After</ToggleButton>
            <ToggleButton value="stacked">Stacked</ToggleButton>
            <ToggleButton value="source">Source</ToggleButton>
          </ToggleButtonGroup>
          <IconButton
            size="small"
            aria-label="Expand diagram comparison"
            title="Expand comparison"
            onClick={() => setExpanded(true)}
          >
            <OpenInFullIcon sx={{ fontSize: 15 }} />
          </IconButton>
        </Stack>
      </div>

      <ComparisonPanels
        viewType={viewType}
        beforeCode={beforeCode}
        afterCode={afterCode}
        beforeLabel={beforeLabel}
        afterLabel={afterLabel}
        mode={mode}
      />

      <DiagramDialog
        open={expanded}
        onClose={() => setExpanded(false)}
        title={`${beforeLabel} → ${afterLabel}`}
        mode={mode}
        comparison={{
          beforeCode,
          afterCode,
          beforeLabel,
          afterLabel,
        }}
      />
    </div>
  );
}
