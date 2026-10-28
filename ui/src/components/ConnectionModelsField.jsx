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

import { useEffect, useState } from "react";
import {
  Autocomplete,
  Box,
  Button,
  Chip,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import api from "../api.js";
import { identifierField } from "../identifierField.js";
import { DISCOVERING_FRAMEWORKS, NO_MODELS_LABEL } from "../state.js";

// ConnectionModelsField is what one connection says about models: what
// its provider lists (read-only, with a refresh), and what it advertises,
// which is what dispatch matches a task's model against
// (docs/connections-and-models.md). A connection does not choose a model
// or an effort -- those belong to the team and the task -- so there is no
// picker for either here.
//
// A connection whose provider can be asked advertises everything it
// lists by default, with nothing to save: grain keeps that list live
// (`discovered`). The operator may exclude models from it
// (`excludedModels`), or pin the connection to a fixed list (`models`).
// A provider that cannot be asked (a custom command) is only ever the
// list typed in.
export default function ConnectionModelsField({
  backend,
  onChange,
  refreshKey = 0,
}) {
  const [catalog, setCatalog] = useState(null);
  const [loading, setLoading] = useState(false);
  const [revision, setRevision] = useState(0);
  // Pin to a fixed list was chosen before the provider listed anything:
  // the list to type into starts empty, which `models` alone cannot say.
  const [pinRequested, setPinRequested] = useState(false);
  const framework = backend.framework;
  const discoverable = DISCOVERING_FRAMEWORKS.includes(framework);

  useEffect(() => {
    if (!discoverable) {
      setCatalog(null);
      setLoading(false);
      return undefined;
    }
    let live = true;
    setLoading(true);
    const params = new URLSearchParams();
    // Antigravity is the framework this route was added for and still
    // its unnamed default, so only the others name themselves.
    if (framework !== "antigravity") params.set("framework", framework);
    if (backend.id) params.set("backend", backend.id);
    const query = params.size ? `?${params}` : "";
    Promise.resolve()
      .then(() => api(`/api/agent-models${query}`))
      .then((data) => {
        if (live) setCatalog(data || {});
      })
      .catch((error) => {
        if (live) setCatalog({ available: true, error: error.message });
      })
      .finally(() => {
        if (live) setLoading(false);
      });
    return () => {
      live = false;
    };
  }, [discoverable, framework, refreshKey, revision, backend.id]);

  const live = (catalog?.models || []).map((m) => m.id).filter(Boolean);
  // What the provider reports: the listing just fetched, else the one
  // grain kept from its last answer -- which is what dispatch reads.
  const stored = backend.discovered?.models || [];
  const discovered = live.length ? live : stored;
  const listed = backend.models || [];
  const excluded = backend.excludedModels || [];
  const pinned =
    !discoverable || listed.length > 0 || !!backend.model || pinRequested;
  const advertised = discovered.filter((id) => !excluded.includes(id));

  return (
    <Stack spacing={1.5}>
      {discoverable && (
        <Box>
          <Stack
            direction="row"
            spacing={1}
            alignItems="center"
            justifyContent="space-between"
            sx={{ mb: 0.75 }}
          >
            <Typography variant="subtitle2">Discovered models</Typography>
            <Button
              type="button"
              size="small"
              variant="outlined"
              disabled={loading}
              onClick={() => setRevision((r) => r + 1)}
            >
              {loading ? "Refreshing…" : "Refresh"}
            </Button>
          </Stack>
          <DiscoveryStatus
            loading={loading}
            catalog={catalog}
            discovered={live}
            advertised={pinned ? listed : advertised}
          />
          <StoredDiscovery discovered={backend.discovered} />
        </Box>
      )}
      {!pinned && (
        <Box>
          <Typography variant="subtitle2" sx={{ mb: 0.75 }}>
            Advertised models
          </Typography>
          <Typography variant="body2" color="text.secondary" sx={{ mb: 0.75 }}>
            {discovered.length
              ? "Every model the provider lists, kept up to date as it changes. Remove one to exclude it."
              : `Every model the provider lists, once it answers. Until then this connection has ${NO_MODELS_LABEL}: grain does not guess a model name, and tasks that would run on it wait.`}
          </Typography>
          <Stack
            direction="row"
            spacing={0.75}
            useFlexGap
            flexWrap="wrap"
            aria-label="Advertised models"
          >
            {advertised.map((id) => (
              <Chip
                key={id}
                size="small"
                label={id}
                onDelete={() => onChange({ excludedModels: [...excluded, id] })}
              />
            ))}
          </Stack>
          {excluded.length > 0 && (
            <Stack
              direction="row"
              spacing={0.75}
              useFlexGap
              flexWrap="wrap"
              alignItems="center"
              sx={{ mt: 1 }}
              aria-label="Excluded models"
            >
              <Typography variant="caption" color="text.secondary">
                Excluded:
              </Typography>
              {excluded.map((id) => (
                <Chip
                  key={id}
                  size="small"
                  variant="outlined"
                  label={id}
                  title="Excluded: click to advertise it again"
                  onDelete={() =>
                    onChange({
                      excludedModels: excluded.filter((m) => m !== id),
                    })
                  }
                />
              ))}
            </Stack>
          )}
          <Button
            type="button"
            size="small"
            variant="text"
            sx={{ mt: 0.5 }}
            onClick={() => {
              setPinRequested(true);
              onChange({ models: advertised });
            }}
          >
            Pin to a fixed list instead
          </Button>
        </Box>
      )}
      {pinned && (
        <Autocomplete
          multiple
          freeSolo
          size="small"
          options={discovered}
          value={listed}
          onChange={(_event, values) =>
            onChange({
              models: Array.from(
                new Set(values.map((v) => String(v).trim()).filter(Boolean)),
              ),
            })
          }
          renderTags={(values, getTagProps) =>
            values.map((option, index) => {
              const { key, ...rest } = getTagProps({ index });
              return (
                <Chip
                  key={key}
                  size="small"
                  label={option}
                  variant={discovered.includes(option) ? "filled" : "outlined"}
                  {...rest}
                />
              );
            })
          }
          renderInput={(params) => (
            <TextField
              {...params}
              label="Advertised models"
              helperText={
                discoverable
                  ? "Pinned: this connection advertises exactly these, and nothing its provider adds later. Type a name the provider does not list, such as an alias, to add it by hand."
                  : "The models this connection serves. A task's model is matched against this list. A custom command cannot be asked, so list them here."
              }
              inputProps={{ ...params.inputProps, ...identifierField }}
            />
          )}
        />
      )}
      {!discoverable && listed.length === 0 && (
        <Typography
          variant="caption"
          color="warning.main"
          data-testid="connection-no-models"
        >
          Nothing listed: {NO_MODELS_LABEL}. grain does not guess a model name,
          so tasks that would run on this connection wait until one is listed
          here.
        </Typography>
      )}
      {pinned && discoverable && (
        <Button
          type="button"
          size="small"
          variant="text"
          sx={{ alignSelf: "flex-start" }}
          onClick={() => {
            setPinRequested(false);
            onChange({ models: [], model: undefined });
          }}
        >
          Advertise all models
        </Button>
      )}
    </Stack>
  );
}

// StoredDiscovery says when grain last heard from the provider, and why
// it could not since -- the list dispatch is using meanwhile.
function StoredDiscovery({ discovered }) {
  if (!discovered) return null;
  const at =
    discovered.refreshedAt && !discovered.refreshedAt.startsWith("0001")
      ? new Date(discovered.refreshedAt).toLocaleString()
      : "";
  return (
    <Stack spacing={0.25} sx={{ mt: 0.75 }}>
      {at && (
        <Typography variant="caption" color="text.secondary">
          Provider&apos;s list last refreshed {at}.
        </Typography>
      )}
      {discovered.error && (
        <Typography variant="caption" color="warning.main">
          {discovered.models?.length
            ? `Could not refresh it since (${discovered.error}); dispatch uses the last list.`
            : `The provider could not be asked (${discovered.error}).`}
        </Typography>
      )}
    </Stack>
  );
}

function DiscoveryStatus({ loading, catalog, discovered, advertised }) {
  if (loading && !catalog) {
    return (
      <Typography variant="body2" color="text.secondary">
        Asking the provider which models it serves…
      </Typography>
    );
  }
  if (!catalog || catalog.available === false) {
    return (
      <Typography variant="body2" color="text.secondary">
        This deployment cannot ask this provider for its models. List them under
        Advertised models.
      </Typography>
    );
  }
  if (catalog.error) {
    return (
      <Typography variant="body2" color="error">
        Could not list this provider&apos;s models: {catalog.error}
      </Typography>
    );
  }
  if (discovered.length === 0) {
    return (
      <Typography variant="body2" color="text.secondary">
        The provider listed no models.
      </Typography>
    );
  }
  return (
    <Stack
      direction="row"
      spacing={0.75}
      useFlexGap
      flexWrap="wrap"
      aria-label="Discovered models"
    >
      {discovered.map((id) => (
        <Chip
          key={id}
          size="small"
          label={id}
          variant={advertised.includes(id) ? "filled" : "outlined"}
          title={
            advertised.includes(id)
              ? "Advertised by this connection"
              : "Listed by the provider, not advertised"
          }
        />
      ))}
    </Stack>
  );
}
