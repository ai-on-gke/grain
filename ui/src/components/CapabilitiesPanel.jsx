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

import { useState } from "react";
import {
  Alert,
  AlertTitle,
  Box,
  Button,
  ButtonBase,
  Chip,
  Link,
  Stack,
  Tooltip,
  Typography,
} from "@mui/material";
import { SecretFields } from "./SecretField.jsx";
import api from "../api.js";
import { useTimeZone } from "../TimeZoneContext.jsx";
import { formatDateTime } from "../time.js";

// The guide this tab and Settings → Capabilities share
// (docs/capability-servers-ui.md), which says how the two differ.
const CAPABILITIES_GUIDE =
  "https://github.com/ai-on-gke/grain/blob/main/docs/capability-servers-ui.md#legacy-capabilities";

// chipHelp is what each chip on this tab means, shown as its tooltip
// and, for the open capability, written out under it.
export const chipHelp = {
  ready:
    "This deployment has every setting and secret this capability needs, so a task granted it can be given it.",
  notReady:
    "Something this capability needs is missing; what it is is listed on its page. A task granted it fails to start until that is fixed.",
  ciOnly:
    "Granted only to a repository's CI jobs, never to a task: its credential can write to the repository.",
  notGrantable:
    "grain runs a provider for this, but no task can be granted it. Configuration here cannot change that.",
  default:
    "Every new task is filed already holding this. Which capabilities do is chosen in Settings → Tasks, under Default capabilities.",
  buildDefault:
    "This version of grain suggests it as a default; a new install starts with it. It is not given to anything until it is chosen.",
  defaultRepos:
    "Every new task filed against these repositories starts holding this; set on each repository's page, under Capabilities.",
};

// HelpChip is a chip whose meaning is its tooltip, and its accessible
// description.
function HelpChip({ help, ...props }) {
  return (
    <Tooltip title={help} arrow>
      <Chip size="small" aria-description={help} tabIndex={0} {...props} />
    </Tooltip>
  );
}

// CapabilityChips are the facts about one capability a row and its page
// both show.
function CapabilityChips({ cap }) {
  return (
    <Stack
      direction="row"
      spacing={0.75}
      alignItems="center"
      sx={{ flexWrap: "wrap", rowGap: 0.75 }}
    >
      <HelpChip
        label={cap.ready ? "Ready" : "Not ready"}
        color={cap.ready ? "success" : "default"}
        variant={cap.ready ? "filled" : "outlined"}
        help={cap.ready ? chipHelp.ready : chipHelp.notReady}
        data-testid="legacy-capability-ready"
      />
      {/* Two ways a capability can be ungrantable, and they are not
          the same news: "CI only" is by design (its credential writes
          to the repository, so it is released to a release job and
          never to a task), while "Not grantable" is grain's own
          listings having drifted apart. */}
      {cap.ciOnly && (
        <HelpChip
          label="CI only"
          color="info"
          variant="outlined"
          help={chipHelp.ciOnly}
        />
      )}
      {cap.grantable === false && !cap.ciOnly && (
        <HelpChip
          label="Not grantable"
          color="warning"
          variant="filled"
          help={chipHelp.notGrantable}
        />
      )}
      {cap.default && (
        <HelpChip
          label="Default"
          color="info"
          variant="outlined"
          help={chipHelp.default}
        />
      )}
      {/* This build's own default (capability.Registration.DefaultOn),
          which a fresh install starts with and an existing deployment
          is offered on the Default capabilities picker, never given. */}
      {cap.buildDefault && (
        <HelpChip
          label="On by default for this build"
          color="info"
          variant="outlined"
          help={chipHelp.buildDefault}
        />
      )}
      {/* Shown alongside "Default", not instead of it: a repo can
          restate one the deployment already gives, and dropping
          it deployment-wide leaves the repo's own entry standing. */}
      {(cap.defaultRepos || []).length > 0 && (
        <HelpChip
          label={`Default in ${cap.defaultRepos.length} repo${cap.defaultRepos.length === 1 ? "" : "s"}`}
          color="info"
          variant="outlined"
          help={`${chipHelp.defaultRepos} (${cap.defaultRepos.join(", ")})`}
        />
      )}
    </Stack>
  );
}

// grain/task-172: the one control on this pane that asks a question of
// somebody else's API rather than reading grain's own configuration.
//
// "Ready" above means *configured* -- a project, an account and a secret
// are all set -- and that is the whole of what any configuration pane
// can see. The key inside a set secret can have been deleted or rotated
// away at the far end months ago and every one of those facts stays
// true, which is how a deployment sat **Ready** while every mint failed
// with `invalid_grant` (README.md's "Debugging `gcp-key` again"). This
// button authenticates as that standing credential and makes one cheap,
// harmless call with it -- a listing, never a mint -- and shows what
// came back.
//
// The answer is shown beside the badge, never folded into it. It is true
// at the moment it was given and says so; nothing here is stored, and a
// reload clears it, because a stale "checked ok" would be the same
// unfalsifiable reassurance the badge alone already gives.
function CredentialCheck({ capability, showError }) {
  const [check, setCheck] = useState(null);
  const [busy, setBusy] = useState(false);
  const zone = useTimeZone();

  const run = async () => {
    setBusy(true);
    // The verdict on screen is about the check before this one. Drop it
    // now, so a re-check that never gets an answer leaves the error
    // banner on its own rather than an earlier "Credential works" that
    // nothing has just confirmed.
    setCheck(null);
    try {
      setCheck(
        await api(
          `/api/capabilities/${encodeURIComponent(capability.id)}/check`,
          { method: "POST" },
        ),
      );
    } catch (err) {
      // A request that never got an answer is grain's own problem (an
      // unwired deployment, an id this build does not know), so it goes
      // to the error banner rather than being drawn as a verdict about
      // the credential.
      if (showError) showError(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Box sx={{ mt: 1 }}>
      <Button
        type="button"
        size="small"
        variant="outlined"
        disabled={busy}
        onClick={run}
      >
        {busy ? "Testing…" : "Test credential"}
      </Button>
      <Typography variant="body2" className="hint" sx={{ mt: 0.5 }}>
        Makes one harmless call as this capability&apos;s standing credential --
        a listing, never a mint -- and reports what the service said.
        &quot;Ready&quot; above only means this deployment is configured for it.
      </Typography>
      {check && (
        <Alert severity={check.ok ? "success" : "warning"} sx={{ mt: 1 }}>
          {/* Named as the deployment's own credential being refused,
              not as grain failing: the remedy is pasting a current
              value into the field below, and the sentence under this
              says which secret and where. */}
          <AlertTitle>
            {check.ok ? "Credential works" : "Credential refused"}
          </AlertTitle>
          {check.detail}
          <Typography variant="caption" component="div" sx={{ mt: 0.5 }}>
            {(check.credentials || []).length > 0 && (
              <>checked as {check.credentials.join(", ")} · </>
            )}
            {check.checkedAt
              ? formatDateTime(check.checkedAt, zone)
              : "just now"}{" "}
            -- a point-in-time answer, not a badge
          </Typography>
        </Alert>
      )}
    </Box>
  );
}

// ReadyDot is one capability's readiness, small enough to ride at the
// head of its row in the list: filled where this deployment is configured for it,
// a hollow ring where it is not. It carries its own accessible name
// rather than being decorative like ItemGlyph, because the dot *is* the
// information -- a strip of names with nothing announced beside them
// would tell a screen reader nothing about which are configured, which
// is half of what the list is for.
function ReadyDot({ ready }) {
  return (
    <Box
      component="span"
      role="img"
      aria-label={ready ? "configured" : "not configured"}
      title={ready ? "Configured" : "Not configured"}
      sx={{
        width: 8,
        height: 8,
        flex: "none",
        borderRadius: "50%",
        bgcolor: ready ? "success.main" : "transparent",
        border: ready ? "none" : "1px solid",
        borderColor: "warning.main",
      }}
    />
  );
}

// CapabilityDetail is everything one capability has to say, and the one
// place its own credentials are set: the page its row in the list
// opens. It was a card in a stack of them until
// grain/task-355 -- readable at four capabilities, and this deployment
// ships fourteen plus one per named GitHub token, so the gap an
// operator came here to close was several screens down a scroll with
// nothing to say which way.
function CapabilityDetail({ cap, showError, onSecretsChanged }) {
  return (
    <Box>
      <Typography variant="h6" component="h4" sx={{ overflowWrap: "anywhere" }}>
        {cap.name || cap.id}
      </Typography>
      <Typography
        variant="body2"
        color="text.secondary"
        sx={{ mb: 1 }}
        title="The name a task, template or schedule grants this capability by."
      >
        Granted as <code>{cap.id}</code>
      </Typography>
      <CapabilityChips cap={cap} />
      <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5 }}>
        {cap.description}
      </Typography>
      {/* Ahead of the two "missing ..." hints deliberately: this one
          cannot be fixed by anything on this pane, so an operator who
          reads no further than the first line under a capability is
          the one who most needs to see it. */}
      {cap.ciOnly && (
        <Typography variant="body2" className="hint" sx={{ mt: 0.5 }}>
          Granted to a repository&apos;s CI jobs, never to a task: its
          credential can write to the repository itself, so a job receives it
          only for a post-merge release or a run an operator dispatched by hand.
          Turn it on per repository, on the repos page under CI capabilities.
        </Typography>
      )}
      {cap.grantable === false && !cap.ciOnly && (
        <Typography variant="body2" className="hint" sx={{ mt: 0.5 }}>
          No task can be granted this: grain registers a provider for it, but
          the task capability picker does not offer it, so attaching it is
          rejected as an unknown capability. Configuration cannot fix this one.
        </Typography>
      )}
      {cap.default && cap.ready === false && (
        <Typography variant="body2" className="hint" sx={{ mt: 0.5 }}>
          Every new task is filed holding this, and it is not ready: each of
          those tasks will fail to dispatch until the gap below is closed, or
          this capability is dropped from Default capabilities in Settings →
          Tasks.
        </Typography>
      )}
      {(cap.defaultRepos || []).length > 0 && (
        <Typography variant="body2" className="hint" sx={{ mt: 0.5 }}>
          Defaulted on: {cap.defaultRepos.join(", ")} -- every task filed
          against one of those repos starts holding this
          {cap.ready === false
            ? ", and will fail to dispatch until the gap below is closed"
            : ""}
          . Change it on the repos page, under that repo&apos;s Capabilities.
        </Typography>
      )}
      {(cap.missingConfig || []).length > 0 && (
        <Typography variant="body2" className="hint" sx={{ mt: 0.5 }}>
          Needs: {cap.missingConfig.join(", ")} -- deployment settings, typed in
          under Deployment-wide settings on this tab&apos;s first page, not
          here.
        </Typography>
      )}
      {/* Beside "Needs:" and not folded into it: everything this
          one names is already typed in, and the thing to do about
          it is not on this pane at all
          (ui.CapabilityStatus.PendingConfig). */}
      {(cap.pendingConfig || []).length > 0 && (
        <Typography variant="body2" className="hint" sx={{ mt: 0.5 }}>
          Saved, but not yet in effect: {cap.pendingConfig.join(", ")} -- the
          daemon behind this page is still running without it, so a task granted
          this fails to resolve and &quot;Test credential&quot; below finds no
          provider to test. A running daemon adopts a change within one poll
          interval; one that never does has stopped reconciling (Status) and
          needs restarting.
        </Typography>
      )}
      {(cap.missingSecrets || []).length > 0 && (
        <Typography variant="body2" className="hint" sx={{ mt: 0.5 }}>
          Missing secrets: {cap.missingSecrets.join(", ")}
        </Typography>
      )}
      {/* Last in the row, under everything that says what is
          wrong: the fields are what to do about it, and a
          capability with nothing missing still gets them so a
          credential can be rotated or cleared from the same
          place it was set. */}
      <SecretFields
        secrets={cap.secrets}
        showError={showError}
        onChanged={onSecretsChanged}
      />
      {/* Under the fields, because a refused check is answered by
          typing into one of them -- and offered only where this
          deployment can actually make the call (cap.checkable),
          so no pane ever shows a button that could not work. */}
      {cap.checkable && (
        <CredentialCheck capability={cap} showError={showError} />
      )}
    </Box>
  );
}

// CapabilityRow is one capability in the list: the full width of the
// pane, its name, what it does and its chips. It opens its page.
function CapabilityRow({ cap, onOpen }) {
  return (
    <ButtonBase
      onClick={() => onOpen(cap.id)}
      data-legacy-capability-row={cap.id}
      aria-label={`${cap.name || cap.id}, ${cap.ready ? "configured" : "not configured"}`}
      sx={{
        display: "grid",
        gridTemplateColumns: {
          xs: "auto minmax(0, 1fr)",
          md: "auto minmax(0, 1fr) auto",
        },
        columnGap: 1.25,
        rowGap: 0.75,
        alignItems: "start",
        textAlign: "left",
        width: "100%",
        p: 1.5,
        border: 1,
        borderColor: "divider",
        borderRadius: 1,
        "&:hover, &.Mui-focusVisible": { bgcolor: "action.hover" },
      }}
    >
      <Box sx={{ mt: 0.75 }}>
        <ReadyDot ready={!!cap.ready} />
      </Box>
      <Box sx={{ minWidth: 0, overflowWrap: "anywhere" }}>
        <Typography variant="body1" sx={{ fontWeight: 600 }}>
          {cap.name || cap.id}
        </Typography>
        {cap.description && (
          <Typography variant="body2" color="text.secondary">
            {cap.description}
          </Typography>
        )}
      </Box>
      <Box sx={{ gridColumn: { xs: "2", md: "auto" } }}>
        <CapabilityChips cap={cap} />
      </Box>
    </ButtonBase>
  );
}

// bwsalmon/agents#611: a view of GET /api/settings' own "capabilities"
// field -- every capability grain ships a provider for, and whether this
// deployment currently has enough configuration for a task granted one
// to actually work. It takes settings straight from SettingsOverlay
// rather than fetching its own copy, unlike SecretsPanel/UpgradePanel:
// the data is already loaded by the time a tab can be clicked.
//
// It opens on a list, one full-width row per capability, each carrying
// the dot and chips saying whether that one is configured, followed by
// the deployment-wide settings; a row opens that capability's own page,
// with its credentials and its check against the service that issued
// them. It was a strip of sub-tabs (grain/task-355), which squeezed
// fourteen names into boxes too small to read, and a stack of cards
// before that, where the one an operator came for was screens down.
//
// The list is in the API's own order, which is grain's catalog order,
// and deliberately not sorted with the unconfigured ones first: setting
// a secret makes a capability ready, and a list that re-sorted itself on
// that would move every other row under the cursor of whoever had just
// fixed one. The count and the names of what is left sit above it.
//
// The one thing here that is not read-only is a capability's own secrets
// (grain/task-110): each page carries a write-only field per credential
// the capability resolves (cap.secrets), so the pane that reports one
// missing is the pane that fills it in. A value written there changes
// nothing this component holds, so it asks SettingsOverlay to re-read
// settings through onSecretsChanged; without a colocated secrets store
// the API reports no secrets at all and no field is offered.
//
// The list is not quite fixed: a deployment's extra named GitHub tokens
// are capabilities too (grain/task-117, "github-credential:<name>"), and
// arrive here in the same shape as everything else.
//
// `deploymentWide` is what this tab has to say that is not about one
// capability -- the GitHub host and GCP fields that satisfy gcp-key and
// gemini-key, and the leftover secrets nothing on the pane claims. It is
// handed in as a node rather than built here because it is
// SettingsOverlay's own form, with that pane's save behind it.
//
// cap.defaultRepos is the second layer of the default-capabilities
// choice, made on the repos page one repo at a time
// (model.RepoConfig.DefaultCapabilities). It is reported beside
// cap.default rather than folded into it: with two layers, a single
// "Default" chip would describe a deployment-wide default that only some
// tasks actually get.
export default function CapabilitiesPanel({
  capabilities,
  showError,
  onSecretsChanged,
  deploymentWide,
}) {
  const list = capabilities || [];
  // The open capability, by id, or null for the list. Held by id so a
  // settings reload -- one happens on every secret written here --
  // keeps the capability that was open, and drops back to the list if
  // this build has stopped reporting it.
  const [selected, setSelected] = useState(null);
  const cap = selected ? list.find((c) => c.id === selected) : null;
  const readyCount = list.filter((c) => c.ready).length;
  const notReady = list.filter((c) => !c.ready).map((c) => c.name || c.id);

  if (cap) {
    return (
      <Box data-legacy-capability={cap.id}>
        <Button
          size="small"
          onClick={() => setSelected(null)}
          data-testid="legacy-capabilities-back"
          sx={{ mb: 1, ml: -0.5 }}
        >
          ← All legacy capabilities
        </Button>
        <CapabilityDetail
          cap={cap}
          showError={showError}
          onSecretsChanged={onSecretsChanged}
        />
      </Box>
    );
  }

  return (
    <>
      <Box data-testid="legacy-capabilities-intro" sx={{ mb: 1.5 }}>
        <Typography variant="body2" sx={{ mb: 1 }}>
          These are the capabilities built into grain itself, from before
          capability servers. A task, template or schedule is granted one by its
          bare name, such as <code>gcp-key</code>, and grain hands it over
          itself; this tab is where each one&apos;s settings and credentials are
          set. Open one to see what it is short of, set its credentials, and,
          where it holds a standing credential, check that the stored value
          still works.
        </Typography>
        <Alert
          severity="info"
          sx={{ mb: 1 }}
          data-testid="legacy-capabilities-vs-servers"
        >
          Not what you are looking for? Capabilities granted as{" "}
          <code>&lt;capability ID&gt;#&lt;method&gt;</code> come from capability
          servers, which are on{" "}
          <Link href="/settings/capabilities">Settings → Capabilities</Link>.
          The agent frameworks&apos; keys are on the Models tab.{" "}
          <Link href={CAPABILITIES_GUIDE} target="_blank" rel="noopener">
            How the two tabs differ
          </Link>
        </Alert>
        <Alert severity="info">
          Sandboxes clone and push with the deployment&apos;s git connections,
          set in Settings, Git connections. No GitHub credential here is a
          fallback for that work: the ones on this page are grain&apos;s own --
          for pull requests, the merge queue, webhooks and grain CI -- and a
          named token here reaches a task only when it is granted to that task
          as a capability.
        </Alert>
      </Box>
      {list.length === 0 ? (
        <Alert severity="info" sx={{ mb: 1.5 }}>
          No capabilities known.
        </Alert>
      ) : (
        <>
          {/* The count and the names behind it, so "which of these
              still need me" is answered in words, not only in dots. */}
          <Typography variant="body2" className="hint" sx={{ mb: 1 }}>
            {readyCount} of {list.length} configured
            {notReady.length > 0
              ? `. Still to configure: ${notReady.join(", ")} -- open one to see what it needs.`
              : ". Every capability this deployment ships is configured."}
          </Typography>
          <Stack
            spacing={1}
            role="list"
            aria-label="Legacy capabilities"
            sx={{ mb: 2 }}
          >
            {list.map((c) => (
              <Box role="listitem" key={c.id}>
                <CapabilityRow cap={c} onOpen={setSelected} />
              </Box>
            ))}
          </Stack>
        </>
      )}
      {deploymentWide && (
        <Box
          component="section"
          aria-label="Deployment-wide settings"
          data-testid="legacy-capabilities-deployment-wide"
          sx={{ pt: 1.5, borderTop: 1, borderColor: "divider" }}
        >
          <Typography variant="subtitle2" component="h4">
            Deployment-wide settings
          </Typography>
          <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
            Settings more than one capability above reads -- the GitHub host,
            and the GCP project and service accounts gcp-key and gemini-key mint
            from -- and the secrets no capability&apos;s own page claims.
          </Typography>
          {deploymentWide}
        </Box>
      )}
    </>
  );
}
