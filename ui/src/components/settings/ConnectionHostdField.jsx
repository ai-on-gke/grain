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

import { MenuItem, Stack, TextField } from "@mui/material";
import { useEffect, useState } from "react";
import api from "../../api.js";

// LABELS is the picker's value for "a hostd selected by its labels",
// whose selector is typed beside it.
const LABELS = "\u0000labels";

// hostdOption is how one registry row reads in the picker: its name,
// whether it is connected, its owner and the host user it runs as.
export function hostdOption(h) {
  const parts = [h.name];
  const c = h.connection;
  parts.push(
    c?.connected || h.name === "local" ? "connected" : "not connected",
  );
  if (h.owner) parts.push(`owner ${h.owner}`);
  if (c?.principal) parts.push(`runs as ${c.principal}`);
  if (c?.host) parts.push(`on ${c.host}`);
  return parts.join(" · ");
}

// ConnectionHostdField is a connection's placement (model.AgentBackend.
// Hostd, HostdFallback): which grain hostd its agent CLI -- and the CLI
// login an ambient credential is -- runs on. Empty leaves the CLI where
// the framework's run location says. The picker lists the registry
// (GET /api/hostds); "label selector" takes k=v[,k=v] for any hostd
// carrying them. Fallback is the ordered list a run tries while the
// hostd is away, empty unless somebody fills it: without one, a run on
// this connection waits rather than going anywhere else.
//
// A run: hostd capability server's placement (protocol.ServerConfig.
// Hostd) is picked with the same control, without a fallback: noFallback,
// with its own emptyLabel and helperText.
export default function ConnectionHostdField({
  backend,
  onChange,
  noFallback = false,
  emptyLabel = "Where its framework's run location says",
  helperText = "Which grain hostd this connection's CLI, and the CLI login an " +
    "ambient credential is, runs on. While it is not connected the " +
    "connection is unavailable and its runs wait.",
  disabled = false,
  testId = "connection-hostd",
}) {
  const [hostds, setHostds] = useState(null);
  useEffect(() => {
    let live = true;
    api("/api/hostds")
      .then((rows) => live && setHostds(Array.isArray(rows) ? rows : []))
      .catch(() => live && setHostds([]));
    return () => {
      live = false;
    };
  }, []);
  const selector = backend.hostd || "";
  const listed = (hostds || []).filter((h) => !h.revoked_at);
  const named = listed.some((h) => h.name === selector);
  const [labelled, setLabelled] = useState(
    () => selector !== "" && selector.includes("="),
  );
  const pick = labelled ? LABELS : selector;
  // Typed as text, so a separator is not eaten as it is typed; each
  // change hands over the list it now says.
  const [fallback, setFallback] = useState(() =>
    (backend.hostdFallback || []).join("; "),
  );
  return (
    <Stack spacing={2} data-testid={testId}>
      <TextField
        select
        size={noFallback ? "small" : undefined}
        disabled={disabled}
        label={noFallback ? "Hostd" : "Run on"}
        value={named || pick === "" || pick === LABELS ? pick : selector}
        onChange={(event) => {
          const v = event.target.value;
          if (v === LABELS) {
            setLabelled(true);
            onChange({ hostd: undefined });
            return;
          }
          setLabelled(false);
          onChange({
            hostd: v || undefined,
            ...(v ? {} : { hostdFallback: undefined }),
          });
        }}
        helperText={helperText}
        SelectProps={{ inputProps: { "data-testid": `${testId}-select` } }}
        fullWidth
      >
        <MenuItem value="">{emptyLabel}</MenuItem>
        {listed.map((h) => (
          <MenuItem key={h.name} value={h.name}>
            hostd {hostdOption(h)}
          </MenuItem>
        ))}
        {selector && !named && !labelled && (
          <MenuItem value={selector}>
            hostd {selector} (not registered)
          </MenuItem>
        )}
        <MenuItem value={LABELS}>A hostd selected by label…</MenuItem>
      </TextField>
      {labelled && (
        <TextField
          size={noFallback ? "small" : undefined}
          disabled={disabled}
          label="Hostd labels"
          placeholder="pool=gpu,zone=b"
          value={selector}
          onChange={(event) =>
            onChange({ hostd: event.target.value.trim() || undefined })
          }
          helperText="label=value[,label=value]: any connected hostd carrying every one"
          inputProps={{ "data-testid": `${testId}-labels` }}
          fullWidth
        />
      )}
      {selector && !noFallback && (
        <TextField
          label="Fallback (optional)"
          placeholder="ws-b; pool=spare"
          value={fallback}
          onChange={(event) => {
            setFallback(event.target.value);
            const list = event.target.value
              .split(";")
              .map((s) => s.trim())
              .filter(Boolean);
            onChange({ hostdFallback: list.length ? list : undefined });
          }}
          helperText={
            "Hostds tried in order, separated by ';', while the one above " +
            "is not connected. " +
            "Empty, the default, means a run waits for it."
          }
          inputProps={{ "data-testid": `${testId}-fallback` }}
          fullWidth
        />
      )}
    </Stack>
  );
}
