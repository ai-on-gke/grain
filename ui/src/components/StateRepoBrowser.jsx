import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Alert,
  Box,
  Button,
  CircularProgress,
  Stack,
  Typography,
} from "@mui/material";
import FolderIcon from "@mui/icons-material/Folder";
import FolderOpenIcon from "@mui/icons-material/FolderOpen";
import DescriptionOutlinedIcon from "@mui/icons-material/DescriptionOutlined";
import api from "../api.js";
import Markdown from "./Markdown.jsx";
import { useTimeZone } from "../TimeZoneContext.jsx";
import { formatDateTime } from "../time.js";

// Reading the config repository from the UI.
//
// That repository is controller-local: no external remote, no forge page,
// nothing a browser can be pointed at. So until this pane existed the
// only ways to read what grain exports -- one JSON file per table, plus
// the memory prose every run in a scope is told as fact before it starts
// -- were to be an agent in a sandbox that had it checked out, or to
// have a shell on the controller. Neither is available to the person
// running the deployment, which is the person the files are about.
//
// Read-only, and said so on the pane: changing any of it is a pull
// request against grain/config, which is this deployment's own model.
// An edit box here would be a box that cannot work.
//
// The commit is on screen for a reason. The working tree is written by
// the export loop on a timer, so what is read here is what the last
// cycle left; a reader drawing a conclusion from a stale file is the
// obvious failure of a pane like this, and "showing ff335f5, exported
// 05:26" plus the button that runs a cycle is the whole of the cure.

// The bytes of one file this pane asks for. The API caps what it will
// serve regardless (pkg/ui's maxBrowseLimit); this is what a reader
// sitting in front of a table dump actually wants to scroll.
const READ_LIMIT = 256 * 1024;

// prettyJSON reformats a table dump for reading. grain writes these
// files compactly enough to diff, which is not the same as legibly, and
// a body that will not parse -- a file cut at the byte bound, most often
// -- is shown as it stands rather than swallowed.
function prettyJSON(text) {
  try {
    return JSON.stringify(JSON.parse(text), null, 2);
  } catch {
    return text;
  }
}

// A listing arrives flat and sorted, which is already a tree: depth is
// the number of slashes, and a path is hidden exactly when one of its
// ancestors is collapsed.
function visibleEntries(entries, collapsed) {
  return entries.filter((entry) => {
    const parts = entry.path.split("/");
    for (let i = 1; i < parts.length; i += 1) {
      if (collapsed.has(parts.slice(0, i).join("/"))) return false;
    }
    return true;
  });
}

function fileSize(bytes) {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

export default function StateRepoBrowser({ showError, status, onSync }) {
  const zone = useTimeZone();
  const [expanded, setExpanded] = useState(false);
  const [tree, setTree] = useState(null);
  const [collapsed, setCollapsed] = useState(() => new Set());
  const [path, setPath] = useState("");
  const [file, setFile] = useState(null);
  const [loading, setLoading] = useState(false);
  const [syncing, setSyncing] = useState(false);
  // A refusal or a missing file belongs beside the file it was asked
  // for, not in the page-wide error bar: it is an answer about what is
  // on this pane, and it says what may be read instead.
  const [fileError, setFileError] = useState("");

  // The head the listing was read at, which is not necessarily the head
  // the status poll reports: a cycle can land between the two, and what
  // this pane must say is which commit the bytes on screen came from.
  const head = tree?.head || status?.head || "";

  const loadTree = useCallback(() => {
    return api("/api/state-repo/files")
      .then(setTree)
      .catch((e) => showError?.(e.message));
  }, [showError]);

  // Read when somebody asks to read it, not on every paint of the
  // Settings page: this is a listing of a working tree, and the pane it
  // hangs off is one people open to change a setting.
  useEffect(() => {
    if (expanded) loadTree();
  }, [expanded, loadTree]);

  // The file most recently asked for. A read that comes back after
  // somebody has clicked on to another file is dropped, or the pane would
  // draw the first file's bytes under the second file's name.
  const wanted = useRef("");

  const open = useCallback(
    (name) => {
      wanted.current = name;
      setPath(name);
      setLoading(true);
      setFileError("");
      api(
        `/api/state-repo/file?path=${encodeURIComponent(name)}&limit=${READ_LIMIT}`,
      )
        .then((got) => {
          if (wanted.current === name) setFile(got);
        })
        .catch((e) => {
          if (wanted.current !== name) return;
          setFile(null);
          setFileError(e.message);
        })
        .finally(() => {
          if (wanted.current === name) setLoading(false);
        });
    },
    [setPath],
  );

  // Exporting now, from the pane that is showing what the last export
  // left: the listing and the open file are both re-read afterwards,
  // since a cycle that changed something changed exactly what is on
  // screen.
  const sync = async () => {
    setSyncing(true);
    try {
      await onSync?.();
      await loadTree();
      if (path) open(path);
    } finally {
      setSyncing(false);
    }
  };

  const entries = useMemo(
    () => visibleEntries(tree?.entries || [], collapsed),
    [tree, collapsed],
  );

  const toggle = (dir) =>
    setCollapsed((was) => {
      const next = new Set(was);
      if (next.has(dir)) next.delete(dir);
      else next.add(dir);
      return next;
    });

  return (
    <Box sx={{ mt: 3 }}>
      <Typography variant="subtitle2">Browse this repository</Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
        What grain exports, as it stands on this host: templates, suites and
        schedules under <code>settings/</code>, in the format a repository
        writes its own in; one JSON file per remaining table under{" "}
        <code>tables/</code>; and under <code>memory/</code> the prose every run
        in that scope is given before it starts. Read-only &mdash; changing any
        of it is a pull request against <code>grain/config</code>.
      </Typography>
      <Button
        type="button"
        size="small"
        variant="outlined"
        sx={{ mb: 1 }}
        onClick={() => setExpanded((was) => !was)}
      >
        {expanded ? "Hide files" : "Show files"}
      </Button>
      {expanded && (
        <Stack
          direction="row"
          spacing={1}
          alignItems="center"
          flexWrap="wrap"
          sx={{ mb: 1 }}
        >
          <Typography variant="caption" color="text.secondary">
            {head ? `showing ${head.slice(0, 7)}` : "nothing exported yet"}
            {status?.syncedAt
              ? `, exported ${formatDateTime(status.syncedAt, zone)}`
              : ""}
          </Typography>
          <Button
            type="button"
            size="small"
            variant="text"
            disabled={syncing}
            onClick={sync}
          >
            Export now
          </Button>
        </Stack>
      )}
      {expanded && tree?.truncated && (
        <Alert severity="info" sx={{ mb: 1 }}>
          This repository holds more files than one listing carries; what is
          below is the beginning of it.
        </Alert>
      )}

      {expanded && (
        <Stack direction={{ xs: "column", md: "row" }} spacing={2}>
          <Box
            sx={{
              minWidth: { md: 280 },
              maxWidth: { md: 360 },
              maxHeight: 420,
              overflow: "auto",
              border: 1,
              borderColor: "divider",
              borderRadius: 1,
              p: 1,
            }}
          >
            {!tree && <CircularProgress size={16} />}
            {tree && entries.length === 0 && (
              <Typography variant="body2" color="text.secondary">
                Nothing has been exported into this repository yet.
              </Typography>
            )}
            {entries.map((entry) => {
              const depth = entry.path.split("/").length - 1;
              const name = entry.path.split("/").pop();
              const isOpen = !collapsed.has(entry.path);
              return (
                <Box
                  key={entry.path}
                  component="button"
                  type="button"
                  title={entry.path}
                  onClick={() =>
                    entry.dir ? toggle(entry.path) : open(entry.path)
                  }
                  sx={{
                    display: "flex",
                    alignItems: "center",
                    gap: 0.5,
                    width: "100%",
                    textAlign: "left",
                    background:
                      entry.path === path ? "action.selected" : "transparent",
                    fontWeight: entry.path === path ? 600 : 400,
                    border: "none",
                    cursor: "pointer",
                    color: "inherit",
                    font: "inherit",
                    py: 0.25,
                    pl: depth * 1.5,
                  }}
                >
                  {entry.dir ? (
                    isOpen ? (
                      <FolderOpenIcon fontSize="inherit" />
                    ) : (
                      <FolderIcon fontSize="inherit" />
                    )
                  ) : (
                    <DescriptionOutlinedIcon fontSize="inherit" />
                  )}
                  <Typography variant="body2" component="span" noWrap>
                    {name}
                  </Typography>
                  {!entry.dir && (
                    <Typography
                      variant="caption"
                      color="text.secondary"
                      component="span"
                      sx={{ ml: "auto", pl: 1 }}
                    >
                      {fileSize(entry.size || 0)}
                    </Typography>
                  )}
                </Box>
              );
            })}
          </Box>

          <Box sx={{ flex: 1, minWidth: 0 }}>
            {!path && (
              <Typography variant="body2" color="text.secondary">
                Choose a file to read it.
              </Typography>
            )}
            {path && (
              <Typography
                variant="caption"
                color="text.secondary"
                component="div"
              >
                {path}
              </Typography>
            )}
            {loading && <CircularProgress size={16} />}
            {/* The refusal is shown as the server wrote it: it names what
              is browsable, which is the one thing a reader who asked for
              something else needs to know. */}
            {fileError && (
              <Alert severity="warning" sx={{ mt: 1 }}>
                {fileError}
              </Alert>
            )}
            {file?.truncated && (
              <Alert severity="info" sx={{ mt: 1 }}>
                Showing the first {fileSize(file.content.length)} of{" "}
                {fileSize(file.size)}. This file is too big to read whole here;
                the rest is in the working tree on the host.
              </Alert>
            )}
            {file?.binary && (
              <Alert severity="info" sx={{ mt: 1 }}>
                This file is not text, so there is nothing to show.
              </Alert>
            )}
            {file && !file.binary && (
              <Box sx={{ mt: 1, maxHeight: 420, overflow: "auto" }}>
                {file.path.endsWith(".md") ? (
                  <Markdown>{file.content}</Markdown>
                ) : (
                  <Typography
                    variant="caption"
                    component="pre"
                    sx={{
                      m: 0,
                      whiteSpace: "pre-wrap",
                      wordBreak: "break-word",
                    }}
                  >
                    {file.path.endsWith(".json")
                      ? prettyJSON(file.content)
                      : file.content}
                  </Typography>
                )}
              </Box>
            )}
          </Box>
        </Stack>
      )}
    </Box>
  );
}
