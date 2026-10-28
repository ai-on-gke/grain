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

import { useEffect, useRef, useState } from "react";
import {
  Box,
  Chip,
  List,
  ListItemButton,
  ListItemText,
  Typography,
} from "@mui/material";
import api from "../api.js";
import Markdown from "./Markdown.jsx";

// The release notes each version of grain shipped with -- docs/releases,
// embedded in the binary this deployment runs (pkg/releasenotes), so they
// are here whether or not this deployment can reach the grain repository.
//
// Newest first, with the version this deployment runs marked; a version's
// notes are read when it is clicked, since the list is the whole history
// and nobody reads all of it at once.
//
// Each version is a disclosure: its button says whether it is expanded
// and which notes it controls, and the notes are brought into view once
// they load -- they render under the whole list, which on a phone or a
// short desktop window starts below the fold, so a tap otherwise changed
// nothing anyone could see.
const NOTES_ID = "release-version-notes";

export default function ReleaseVersions({ showError }) {
  const [listing, setListing] = useState(null);
  const [selected, setSelected] = useState("");
  // loaded is the notes last read and the version they were read for:
  // switching versions renders before the effect below has asked for
  // the new one, and must not show the old one's notes under the new
  // one's name in the meantime.
  const [loaded, setLoaded] = useState(null);
  const note = loaded && loaded.version === selected ? loaded.note : null;
  const notesRef = useRef(null);

  useEffect(() => {
    let live = true;
    (async () => {
      try {
        const listed = await api("/api/release-notes/versions");
        if (live) setListing(listed || {});
      } catch (err) {
        if (live) {
          setListing({});
          showError?.(err);
        }
      }
    })();
    return () => {
      live = false;
    };
  }, [showError]);

  useEffect(() => {
    if (!selected) return undefined;
    let live = true;
    (async () => {
      try {
        const answer = await api(
          `/api/release-notes/versions/${encodeURIComponent(selected)}`,
        );
        if (live) setLoaded({ version: selected, note: answer });
      } catch (err) {
        if (live) showError?.(err);
      }
    })();
    return () => {
      live = false;
    };
  }, [selected, showError]);

  useEffect(() => {
    if (!selected || !note?.markdown) return;
    // block: "nearest" scrolls the least it can; for notes taller than
    // the window that is their start at the top. jsdom has no
    // scrollIntoView, hence the optional call.
    notesRef.current?.scrollIntoView?.({ block: "nearest" });
  }, [selected, note]);

  if (!listing) return null;
  const versions = Array.isArray(listing.versions) ? listing.versions : [];
  const running = listing.running || "";
  const runningListed = versions.some((v) => v.version === running);

  return (
    <Box data-testid="release-notes-versions">
      <Typography variant="subtitle1" component="h4" sx={{ mt: 0 }}>
        Versions
      </Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
        What each release of grain brought, newest first, from the notes built
        into the grain this deployment runs.{" "}
        {running
          ? runningListed
            ? `This deployment runs ${running}.`
            : `This deployment runs ${running}, which has no notes of its own in this build.`
          : "This build does not say which version it is."}
      </Typography>
      {versions.length === 0 ? (
        <Typography variant="body2" color="text.secondary">
          This build carries no release notes.
        </Typography>
      ) : (
        <List dense disablePadding aria-label="grain versions">
          {versions.map((v) => (
            <ListItemButton
              key={v.version}
              selected={v.version === selected}
              aria-expanded={v.version === selected}
              aria-controls={v.version === selected ? NOTES_ID : undefined}
              onClick={() =>
                setSelected(v.version === selected ? "" : v.version)
              }
            >
              <ListItemText
                primary={
                  v.title && v.title !== v.version
                    ? `${v.version} — ${v.title}`
                    : v.version
                }
                secondary={v.date ? `Released ${v.date}` : null}
              />
              {v.version === running && (
                <Chip size="small" color="primary" label="running" />
              )}
            </ListItemButton>
          ))}
        </List>
      )}
      {selected && note?.markdown && (
        <Box
          ref={notesRef}
          id={NOTES_ID}
          role="region"
          aria-label={`${selected} release notes`}
          sx={{ mt: 1 }}
          data-testid="release-version-notes"
        >
          <Markdown>{note.markdown}</Markdown>
        </Box>
      )}
    </Box>
  );
}
