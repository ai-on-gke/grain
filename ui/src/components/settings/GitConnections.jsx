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

import {
  Alert,
  Box,
  Button,
  Chip,
  MenuItem,
  Paper,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  TextField,
  Typography,
} from "@mui/material";
import { useEffect, useMemo, useState } from "react";
import api from "../../api.js";
import {
  distinctConnections,
  repoPinnedConnection,
  resolveRepoConnection,
  splitRepo,
} from "../../gitConnections.js";

// GitConnections is Settings → Git (docs/design/settings-panel.md §3.2):
// the deployment's git connections, and who its agents push and commit
// as (grain/task-25). `deployment` is the deployment's record
// (useDeployment), null while it loads.
//
// The deployment's git access is its git connections and nothing else
// (grain/task-3929): each a credential on one host, for the host as a
// whole, one owner, or one repository -- there is no other credential
// behind them, and a repository no connection reaches has its tasks held
// until one does. Under the connections, the deployment's repositories
// each say which connection they resolve to, with a picker to choose
// another; the single token and per-repository tokens this pane used to
// offer are gone, folded into connections when the daemon starts.
//
// The commit author is never typed in: every run commits as the account
// the git credential it pushes with acts as, derived on the server
// (gitproxy.CredentialSet.CredentialGitIdentity) -- a PAT's own GitHub
// login with its noreply address, a GitHub App's bot account
// (`<slug>[bot]`), an SSH key's paired token's, or an ambient
// connection's host login. Each connection's status carries that
// identity as commitName/commitEmail, and the Commit author section
// lists them; a connection that names nobody (a plain git host, an
// unpaired SSH key) commits as grain's own (deploymentGitName/
// deploymentGitEmail, gitIdentity.js).
//
// canManageTokens is false for a viewer the server would refuse to add,
// test, remove or pick a connection for (a plain member): they see the
// connections and what each repository uses, read-only, rather than
// buttons that answer 403 (grain/task-1995).
export default function GitConnections({
  deployment,
  onDeploymentChanged,
  showError,
  deploymentGitName,
  deploymentGitEmail,
  canManageTokens = true,
}) {
  const [connections, setConnections] = useState([]);
  const authors = useMemo(() => commitIdentities(connections), [connections]);

  if (!deployment) {
    return (
      <Alert severity="info">
        This deployment has not answered with its connections yet, so there is
        nothing to configure here.
      </Alert>
    );
  }

  const grainDefault =
    deploymentGitName || deploymentGitEmail
      ? `${deploymentGitName || "no name"} <${deploymentGitEmail || "no address"}>`
      : "grain's own default";

  return (
    <>
      <GitConnectionsList
        deployment={deployment}
        onChanged={onDeploymentChanged}
        showError={showError}
        readOnly={!canManageTokens}
        onConnections={setConnections}
      />

      <Typography variant="subtitle2" sx={{ mt: 3 }}>
        Commit author
      </Typography>
      <Typography
        variant="body2"
        color="text.secondary"
        sx={{ mb: 1 }}
        data-testid="commit-author-derived"
      >
        Agents commit as the account the git credential they push with acts as,
        so there is nothing to set here: a personal access token commits as its
        owner&apos;s GitHub login (with that login&apos;s noreply address), a
        GitHub App as its bot account, an SSH key as the token paired with it,
        and an ambient connection as this host&apos;s own login. A credential
        that names nobody GitHub can say -- a plain git host, an SSH key with no
        token paired -- commits as {grainDefault}.
      </Typography>
      {authors.length > 0 && (
        <Box component="ul" sx={{ mt: 0, pl: 3 }} data-testid="commit-authors">
          {authors.map((a) => (
            <Typography
              component="li"
              variant="body2"
              key={a.credential}
              data-testid={`commit-author-${a.credential}`}
            >
              <code>{a.host || "github.com"}</code>
              {a.scope ? (
                <>
                  {" "}
                  (<code>{a.scope}</code>)
                </>
              ) : null}
              : {formatCommitAuthor(a, grainDefault)}
            </Typography>
          ))}
        </Box>
      )}
    </>
  );
}

// CREDENTIAL_TYPES are the typed editor's auth types, as the server
// names them (gitproxy.AuthType*).
const CREDENTIAL_TYPES = [
  { value: "pat", label: "PAT" },
  { value: "ssh", label: "SSH key" },
  { value: "app", label: "GitHub App" },
  { value: "none", label: "None (public)" },
];

// AMBIENT_TYPE is the connection type that asks this host's own gh or git
// login for a token when one is needed (gitproxy.AuthTypeAmbient). It is
// offered only on Git connections, where a system admin assigns it.
const AMBIENT_TYPE = {
  value: "ambient",
  label: "Ambient (this host's GitHub login)",
};

// sourcePresets are the hosts the registered ambient sources
// (GET /api/git-ambient-sources, gitproxy.AmbientSourceUI) fill the
// new-connection form in for, each with the source it is for.
const sourcePresets = (sources) =>
  sources.flatMap((src) =>
    (src.presets || []).map((p) => ({ ...p, source: src })),
  );

// autoSourceLabel is the Source menu's "auto" entry: each registered
// source on the hosts it claims, then gh and git credential fill.
const autoSourceLabel = (sources) => {
  const claimed = sources
    .filter((src) => src.hosts?.length)
    .map((src) => `${src.command} on ${src.hosts.join(", ")}`);
  return claimed.length
    ? `auto (${claimed.join("; ")}; gh, then git credential fill elsewhere)`
    : "auto (gh, then git credential fill)";
};

// ambientHelpers names the tools an ambient login may be asked of:
// gh, each registered source's command, and git's credential helper.
const ambientHelpers = (sources) =>
  `${["gh", ...sources.map((src) => src.command)].join(", ")} or git credential helper`;

// SourceHelp is a registered source's help for host: "{host}" is the
// host, and text between backticks is code.
function SourceHelp({ text, host }) {
  return text
    .replaceAll("{host}", host)
    .split("`")
    .map((part, i) =>
      i % 2 ? <code key={i}>{part}</code> : <span key={i}>{part}</span>,
    );
}

// AMBIENT_POLL_MS is how often the connections list asks again for an
// ambient connection whose login the server is still looking up.
const AMBIENT_POLL_MS = 2000;

const typeLabel = (type) =>
  [...CREDENTIAL_TYPES, AMBIENT_TYPE].find((t) => t.value === type)?.label ||
  "credential";

// CredentialCheckResult is one Test's answer: whom GitHub took the token
// for, and a ✓/✗ per repo with whether it can read and push there.
export function CredentialCheckResult({ result, onClose }) {
  const identity = result.app
    ? `GitHub App installation${result.installationRepos ? ` (${result.installationRepos} repositories)` : ""}`
    : result.login
      ? `@${result.login}`
      : "";
  const details = [];
  if (result.saved === false) details.push("not saved yet");
  if (result.scopes?.length)
    details.push(`scopes: ${result.scopes.join(", ")}`);
  if (result.expires) details.push(`expires ${result.expires}`);
  const repos = result.repos || [];
  const allPush = repos.length > 0 && repos.every((r) => r.push);
  const severity = !result.ok
    ? "error"
    : allPush || repos.length === 0
      ? "success"
      : "warning";
  return (
    <Alert
      severity={severity}
      onClose={onClose}
      sx={{ py: 0, px: 1.5, mt: 1 }}
      data-testid="credential-check-result"
    >
      <Box component="span" sx={{ display: "block" }}>
        {result.ok
          ? `Token accepted${identity ? ` as ${identity}` : ""}${result.host ? ` by ${result.host}` : ""}`
          : result.error || "The token was not accepted"}
      </Box>
      {result.ok && details.length > 0 && (
        <Box component="span" sx={{ display: "block" }}>
          {details.join(" · ")}
        </Box>
      )}
      {repos.map((r) => (
        <Box
          component="span"
          key={r.repo}
          sx={{ display: "block" }}
          data-testid={`credential-check-repo-${r.repo}`}
        >
          {r.push ? "✓" : "✗"} <code>{r.repo}</code>{" "}
          {r.read
            ? r.push
              ? "read, push"
              : "read only -- cannot push or open pull requests"
            : r.error || "not visible to this token"}
        </Box>
      ))}
      {result.skipped > 0 && (
        <Box component="span" sx={{ display: "block" }}>
          {result.skipped} more repositories were not checked.
        </Box>
      )}
    </Alert>
  );
}

// deploymentRepos is every repository this deployment works in: its
// declared target repos and the ones its tasks have used (repos), once
// each.
export function deploymentRepos(deployment) {
  const out = [];
  for (const r of deployment?.targetRepos || []) {
    if (r && !out.includes(r)) out.push(r);
  }
  for (const r of deployment?.repos || []) {
    const name = r?.repo;
    if (name && !name.includes("*") && !out.includes(name)) out.push(name);
  }
  return out;
}

// commitIdentities is who each of the deployment's git connections commits
// as, in list order: the server's status.commitName/commitEmail -- the
// account the connection's credential acts as -- with pending set while
// that is still being looked up and error when the credential names
// nobody GitHub can say. A connection with no credential to speak of
// (type None) is left out.
export function commitIdentities(connections) {
  return distinctConnections(connections)
    .filter((c) => c?.credential && c.authType !== "none")
    .map((c) => ({
      credential: c.credential,
      host: c.host || "",
      scope: c.scope || "",
      name: c.status?.commitName || "",
      email: c.status?.commitEmail || "",
      pending: Boolean(c.status?.commitPending || c.status?.ambientPending),
      error: c.status?.commitError || "",
    }));
}

// formatCommitAuthor is one commitIdentities entry as the Commit author
// list says it: `Name <email>`, or what is still being asked, or the
// fallback (grain's own) and why.
export function formatCommitAuthor(author, fallback) {
  if (author.name || author.email) {
    return `${author.name || fallback} <${author.email || "no address"}>`;
  }
  if (author.pending) return "looking up who this credential is…";
  return `${fallback}${author.error ? ` (${author.error})` : ""}`;
}

// GitConnectionsList manages the deployment's git connections -- connections
// to any git host.
// onConnections, when given, is told the list each time it is (re)read,
// so the panel around it can say what else follows from a connection --
// who each connection commits as (commitIdentities).
function GitConnectionsList({
  deployment,
  onChanged,
  showError,
  readOnly = false,
  onConnections,
}) {
  const [connections, setConnections] = useState([]);
  const [loading, setLoading] = useState(true);
  const [reload, setReload] = useState(0);

  useEffect(() => {
    onConnections?.(connections);
  }, [connections, onConnections]);

  // Add form state
  const [adding, setAdding] = useState(false);
  const [host, setHost] = useState("");
  const [kind, setKind] = useState("github");
  const [scope, setScope] = useState("");
  const [authType, setAuthType] = useState("pat");
  const [tokenValue, setTokenValue] = useState("");
  const [appId, setAppId] = useState("");
  const [installationId, setInstallationId] = useState("");
  const [privateKey, setPrivateKey] = useState("");
  const [sshKey, setSshKey] = useState("");
  const [knownHosts, setKnownHosts] = useState("");
  const [apiToken, setApiToken] = useState("");
  const [ambientSource, setAmbientSource] = useState("auto");
  const [proxy, setProxy] = useState("");
  const [preset, setPreset] = useState("");
  const [ambientSources, setAmbientSources] = useState([]);
  const [busy, setBusy] = useState(false);

  // Per-connection test state
  const [testingCred, setTestingCred] = useState(null);
  const [testResult, setTestResult] = useState(null);
  const [actionError, setActionError] = useState(null);

  useEffect(() => {
    let active = true;
    setLoading(true);
    Promise.resolve(api("/api/deployment/git-connections"))
      .then((res) => {
        if (active) {
          setConnections(Array.isArray(res) ? res : []);
          setLoading(false);
        }
      })
      .catch((err) => {
        if (active) {
          setConnections([]);
          setLoading(false);
          showError?.(err);
        }
      });
    return () => {
      active = false;
    };
  }, [deployment.id, reload]);

  // The command-backed forges this deployment registered, for the Source
  // menu and the presets: none is not an error, only nothing to offer.
  useEffect(() => {
    let active = true;
    Promise.resolve(api("/api/git-ambient-sources"))
      .then((res) => {
        if (active && Array.isArray(res)) setAmbientSources(res);
      })
      .catch(() => {});
    return () => {
      active = false;
    };
  }, []);
  const presets = sourcePresets(ambientSources);
  const chosenPreset = presets.find((p) => p.host === preset);

  // An ambient connection's login, and any connection's commit author,
  // is looked up in the background (asking the host's gh or git
  // credential helper, or GitHub's /user, can be slow): while any is
  // still pending, ask again every couple of seconds, without blanking
  // the list, until the server says who it is or why it could not tell.
  const ambientPending = connections.some(
    (c) => c.status?.ambientPending || c.status?.commitPending,
  );
  useEffect(() => {
    if (!ambientPending) return undefined;
    let active = true;
    const timer = setTimeout(() => {
      Promise.resolve(api("/api/deployment/git-connections"))
        .then((res) => {
          if (active && Array.isArray(res)) setConnections(res);
        })
        .catch((err) => active && showError?.(err));
    }, AMBIENT_POLL_MS);
    return () => {
      active = false;
      clearTimeout(timer);
    };
  }, [deployment.id, ambientPending, connections]);

  const testConnection = async (conn) => {
    setTestingCred(conn.credential);
    setTestResult(null);
    try {
      const res = await api("/api/deployment/git-connections/check", {
        method: "POST",
        body: JSON.stringify({
          host: conn.host,
          credential: conn.credential,
          scope: conn.scope,
        }),
      });
      setTestResult({ cred: conn.credential, result: res });
    } catch (err) {
      setTestResult({
        cred: conn.credential,
        result: { ok: false, error: err?.message || String(err) },
      });
    } finally {
      setTestingCred(null);
    }
  };

  // Removing a connection deletes the credential stored for it, which
  // nothing here can read back, so a stray press is asked about first.
  const removeConnection = async (conn) => {
    if (
      !window.confirm(
        `Remove the ${conn.host || "git"} connection ${conn.credential}? A credential stored for it is deleted with it and cannot be read back, so adding it again means pasting that credential again.`,
      )
    )
      return;
    setBusy(true);
    setActionError(null);
    try {
      await api(
        `/api/deployment/git-connections/${encodeURIComponent(conn.credential)}`,
        {
          method: "DELETE",
        },
      );
      setReload((n) => n + 1);
      await onChanged?.();
    } catch (err) {
      setActionError(err?.message || String(err));
      showError?.(err);
    } finally {
      setBusy(false);
    }
  };

  const resetNewConnectionForm = () => {
    setAdding(false);
    setActionError(null);
    setHost("");
    setKind("github");
    setAuthType("pat");
    setAmbientSource("auto");
    setProxy("");
    setPreset("");
    setScope("");
    setTokenValue("");
    setAppId("");
    setInstallationId("");
    setPrivateKey("");
    setSshKey("");
    setKnownHosts("");
    setApiToken("");
  };

  const saveNewConnection = async () => {
    setBusy(true);
    setActionError(null);
    try {
      const body = {
        host: host.trim(),
        kind,
        scope: scope.trim(),
        type: authType,
      };
      if (proxy.trim()) {
        body.proxy = proxy.trim();
      }
      if (authType === "pat") {
        body.value = tokenValue.trim();
      } else if (authType === "app") {
        body.app = {
          appId: appId.trim(),
          installationId: installationId.trim(),
          privateKey,
        };
      } else if (authType === "ssh") {
        body.ssh = { privateKey: sshKey, knownHosts: knownHosts.trim() };
        if (apiToken.trim()) body.apiToken = apiToken.trim();
      } else if (authType === "ambient") {
        body.source = ambientSource;
      }
      await api("/api/deployment/git-connections", {
        method: "POST",
        body: JSON.stringify(body),
      });
      resetNewConnectionForm();
      setReload((n) => n + 1);
      await onChanged?.();
    } catch (err) {
      setActionError(err?.message || String(err));
      showError?.(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Box sx={{ mb: 3 }} data-testid="git-connections-list">
      <Stack
        direction="row"
        justifyContent="space-between"
        alignItems="center"
        sx={{ mb: 1 }}
      >
        <Box>
          <Typography variant="subtitle2">Git connections</Typography>
          <Typography variant="body2" color="text.secondary">
            Connections to github.com, GitHub Enterprise or other git hosts for
            this deployment.
          </Typography>
        </Box>
        {!readOnly && !adding && (
          <Button
            variant="outlined"
            size="small"
            onClick={() => setAdding(true)}
            data-testid="add-connection-button"
          >
            Add connection
          </Button>
        )}
      </Stack>

      {actionError && (
        <Alert
          severity="error"
          onClose={() => setActionError(null)}
          sx={{ mb: 2 }}
          data-testid="git-connections-error"
        >
          {actionError}
        </Alert>
      )}

      {connections.length === 0 && !loading ? (
        <Typography variant="body2" color="text.secondary" sx={{ py: 1 }}>
          No git connections configured.
        </Typography>
      ) : (
        <TableContainer component={Paper} variant="outlined" sx={{ mb: 2 }}>
          <Table size="small" aria-label="Git connections table">
            <TableHead>
              <TableRow>
                <TableCell>Host</TableCell>
                <TableCell>Kind</TableCell>
                <TableCell>Auth Type</TableCell>
                <TableCell>Scope</TableCell>
                <TableCell align="right">Actions</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {distinctConnections(connections).map((conn) => (
                <TableRow
                  key={conn.credential}
                  data-testid={`connection-row-${conn.credential}`}
                  hover
                >
                  <TableCell>
                    <code>{conn.host || "github.com"}</code>
                    {conn.legacy && (
                      <Chip
                        size="small"
                        label="legacy default"
                        sx={{ ml: 1, height: 20 }}
                      />
                    )}
                  </TableCell>
                  <TableCell>{conn.kind || "github"}</TableCell>
                  <TableCell>
                    <Chip
                      size="small"
                      label={typeLabel(conn.authType || "pat")}
                      variant="outlined"
                    />
                    {conn.authType === "ambient" && (
                      <Typography
                        variant="caption"
                        display="block"
                        color={conn.status?.error ? "error" : "warning.main"}
                        data-testid={`connection-ambient-${conn.credential}`}
                      >
                        {conn.status?.error ||
                          (conn.status?.ambientPending &&
                            `checking login… (asking this host's ${ambientHelpers(ambientSources)})`) ||
                          conn.status?.ambientWarning ||
                          "pushes and API calls act as this host's GitHub login, with all of its access"}
                      </Typography>
                    )}
                    {conn.status?.proxy && (
                      <Typography
                        variant="caption"
                        display="block"
                        color="text.secondary"
                        data-testid={`connection-proxy-${conn.credential}`}
                      >
                        via proxy <code>{conn.status.proxy}</code>
                      </Typography>
                    )}
                  </TableCell>
                  <TableCell>
                    {conn.scope ? (
                      <code>{conn.scope}</code>
                    ) : (
                      <Typography variant="caption" color="text.secondary">
                        all repos
                      </Typography>
                    )}
                  </TableCell>
                  <TableCell align="right">
                    <Stack
                      direction="row"
                      spacing={1}
                      justifyContent="flex-end"
                    >
                      {!readOnly && (
                        <Button
                          size="small"
                          variant="outlined"
                          disabled={testingCred === conn.credential || busy}
                          onClick={() => testConnection(conn)}
                        >
                          {testingCred === conn.credential
                            ? "Testing..."
                            : "Test"}
                        </Button>
                      )}
                      {!readOnly && (
                        <Button
                          size="small"
                          color="error"
                          disabled={busy}
                          onClick={() => removeConnection(conn)}
                        >
                          Remove
                        </Button>
                      )}
                    </Stack>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}

      {testResult && (
        <Box sx={{ mb: 2 }}>
          <CredentialCheckResult
            result={testResult.result}
            onClose={() => setTestResult(null)}
          />
        </Box>
      )}

      {adding && (
        <Paper
          variant="outlined"
          sx={{ p: 2, mb: 2 }}
          data-testid="add-connection-form"
        >
          <Typography variant="subtitle2" sx={{ mb: 1 }}>
            New git connection
          </Typography>
          <Stack spacing={2}>
            <Stack direction="row" spacing={1}>
              <TextField
                name="new-connection-host"
                label="Host"
                size="small"
                value={host}
                onChange={(evt) => setHost(evt.target.value)}
                placeholder="github.com or ghe.example.com"
                sx={{ flex: 2 }}
              />
              <TextField
                select
                name="new-connection-kind"
                label="Kind"
                size="small"
                value={kind}
                onChange={(evt) => setKind(evt.target.value)}
                sx={{ flex: 1 }}
              >
                <MenuItem value="github">GitHub</MenuItem>
                <MenuItem value="git">Plain git</MenuItem>
              </TextField>
              <TextField
                select
                name="new-connection-auth-type"
                label="Auth type"
                size="small"
                value={authType}
                onChange={(evt) => setAuthType(evt.target.value)}
                sx={{ flex: 1 }}
              >
                <MenuItem value="pat">PAT</MenuItem>
                <MenuItem value="ssh">SSH key</MenuItem>
                <MenuItem value="app">GitHub App</MenuItem>
                <MenuItem value={AMBIENT_TYPE.value}>
                  {AMBIENT_TYPE.label}
                </MenuItem>
              </TextField>
            </Stack>

            {presets.length > 0 && (
              <TextField
                select
                name="new-connection-preset"
                label="Preset (optional)"
                size="small"
                value={preset}
                onChange={(evt) => {
                  const value = evt.target.value;
                  setPreset(value);
                  const chosen = presets.find((p) => p.host === value);
                  if (chosen) {
                    setHost(chosen.host);
                    setKind("github");
                    setAuthType(AMBIENT_TYPE.value);
                    setAmbientSource(chosen.source.name);
                    setProxy("");
                  }
                }}
                fullWidth
              >
                <MenuItem value="">None</MenuItem>
                {presets.map((p) => (
                  <MenuItem key={p.host} value={p.host}>
                    {p.label}
                  </MenuItem>
                ))}
              </TextField>
            )}
            {chosenPreset?.source.help && (
              <Alert severity="info" data-testid="new-connection-preset-help">
                <SourceHelp
                  text={chosenPreset.source.help}
                  host={chosenPreset.host}
                />
              </Alert>
            )}
            <TextField
              name="new-connection-scope"
              label="Scope (optional)"
              size="small"
              value={scope}
              onChange={(evt) => setScope(evt.target.value)}
              helperText="Owner/organization or owner/repo to narrow this connection. Leave empty for host default."
              fullWidth
            />

            <TextField
              name="new-connection-proxy"
              label="Proxy (optional)"
              size="small"
              value={proxy}
              onChange={(evt) => setProxy(evt.target.value)}
              placeholder="http://127.0.0.1:3128"
              helperText="An http(s)://host:port proxy every connection to this host goes through (no login in it). Other hosts are unaffected."
              fullWidth
            />

            {authType === "pat" && (
              <TextField
                name="new-connection-token"
                label="Token (PAT)"
                type="password"
                size="small"
                value={tokenValue}
                onChange={(evt) => setTokenValue(evt.target.value)}
                placeholder="ghp_… or github_pat_…"
                fullWidth
              />
            )}

            {authType === "ssh" && (
              <Stack spacing={1} data-testid="new-connection-ssh">
                <TextField
                  name="new-connection-ssh-private-key"
                  label="SSH private key"
                  size="small"
                  value={sshKey}
                  onChange={(evt) => setSshKey(evt.target.value)}
                  placeholder="-----BEGIN OPENSSH PRIVATE KEY-----"
                  multiline
                  minRows={2}
                  fullWidth
                />
                <TextField
                  name="new-connection-ssh-known-hosts"
                  label="Known hosts (optional)"
                  size="small"
                  value={knownHosts}
                  onChange={(evt) => setKnownHosts(evt.target.value)}
                  helperText="The host's public key, as a known_hosts line. Needed for any host but github.com: grain never trusts a host key on first use."
                  multiline
                  fullWidth
                />
                <TextField
                  name="new-connection-ssh-api-token"
                  label="API token (optional)"
                  type="password"
                  size="small"
                  value={apiToken}
                  onChange={(evt) => setApiToken(evt.target.value)}
                  helperText="On a GitHub host, the PAT pull requests, checks and merges are made with. Without one, repositories reached with this key are git-only."
                  fullWidth
                />
              </Stack>
            )}

            {authType === "ambient" && (
              <Stack spacing={1} data-testid="new-connection-ambient">
                <TextField
                  select
                  name="new-connection-ambient-source"
                  label="Source"
                  size="small"
                  value={ambientSource}
                  onChange={(evt) => setAmbientSource(evt.target.value)}
                  helperText="Asked for a token each time one is needed, as the account the grain daemon runs as. Nothing is stored."
                  fullWidth
                >
                  <MenuItem value="auto">
                    {autoSourceLabel(ambientSources)}
                  </MenuItem>
                  <MenuItem value="gh">gh auth token</MenuItem>
                  {ambientSources.map((src) => (
                    <MenuItem key={src.name} value={src.name}>
                      {src.label || src.command}
                    </MenuItem>
                  ))}
                  <MenuItem value="git">git credential fill</MenuItem>
                </TextField>
                <Alert severity="warning">
                  Pushes and API calls act as the account this host&apos;s{" "}
                  <code>gh</code> is logged in as, with all of its access — not
                  only this deployment&apos;s repositories. Only a system admin
                  can add one, and only on a daemon running on the host. Prefer
                  a fine-grained PAT or a GitHub App for anything shared.
                </Alert>
              </Stack>
            )}

            {authType === "app" && (
              <Stack spacing={1}>
                <Stack direction="row" spacing={1}>
                  <TextField
                    name="new-connection-app-id"
                    label="App ID"
                    size="small"
                    value={appId}
                    onChange={(evt) => setAppId(evt.target.value)}
                    sx={{ flex: 1 }}
                  />
                  <TextField
                    name="new-connection-app-installation-id"
                    label="Installation ID"
                    size="small"
                    value={installationId}
                    onChange={(evt) => setInstallationId(evt.target.value)}
                    sx={{ flex: 1 }}
                  />
                </Stack>
                <TextField
                  name="new-connection-app-private-key"
                  label="App private key"
                  size="small"
                  value={privateKey}
                  onChange={(evt) => setPrivateKey(evt.target.value)}
                  placeholder="-----BEGIN RSA PRIVATE KEY-----"
                  multiline
                  minRows={2}
                  fullWidth
                />
              </Stack>
            )}

            <Stack direction="row" spacing={1} justifyContent="flex-end">
              <Button
                size="small"
                disabled={busy}
                onClick={resetNewConnectionForm}
              >
                Cancel
              </Button>
              <Button
                size="small"
                variant="contained"
                disabled={
                  busy ||
                  (authType === "pat" && !tokenValue.trim()) ||
                  (authType === "ssh" && !sshKey.trim()) ||
                  (authType === "app" &&
                    (!appId.trim() ||
                      !installationId.trim() ||
                      !privateKey.trim()))
                }
                onClick={saveNewConnection}
              >
                {busy ? "Saving…" : "Save connection"}
              </Button>
            </Stack>
          </Stack>
        </Paper>
      )}

      <RepoConnections
        deployment={deployment}
        connections={connections}
        loading={loading}
        readOnly={readOnly}
        showError={showError}
        onChanged={async () => {
          setReload((n) => n + 1);
          await onChanged?.();
        }}
      />
    </Box>
  );
}

// connectionLabel names a connection for the repository picker: its
// scope on its host and its auth type, since a credential's own name is
// grain's internal one.
function connectionLabel(conn) {
  const host = conn.host || "github.com";
  const where = conn.scope ? `${conn.scope} on ${host}` : `${host} default`;
  return `${where} (${typeLabel(conn.authType || "pat")})`;
}

const MATCH_LABELS = {
  repo: "chosen for this repository",
  owner: "the owner's connection",
  default: "the host's default",
};

// RepoConnections is, for each of the deployment's repositories, the connection
// it resolves to (resolveRepoConnection: repository, then owner, then the
// host's default) and a picker to choose another of the deployment's
// connections on that host. A choice is recorded as that credential
// scoped to the repository (PUT .../repos/{owner}/{name}/git-connection),
// so the connection list stays the one place access is recorded;
// "Default" removes it. A repository nothing reaches says its tasks are
// held.
function RepoConnections({
  deployment,
  connections,
  loading,
  readOnly,
  showError,
  onChanged,
}) {
  const repos = useMemo(() => deploymentRepos(deployment), [deployment]);
  const [savingRepo, setSavingRepo] = useState(null);

  const pick = async (repo, credential) => {
    const { host, owner, name } = splitRepo(repo);
    setSavingRepo(repo);
    try {
      await api(
        `/api/deployment/repos/${encodeURIComponent(owner)}/${encodeURIComponent(name)}/git-connection`,
        {
          method: "PUT",
          body: JSON.stringify({ credential, host }),
        },
      );
      await onChanged?.();
    } catch (err) {
      showError?.(err);
    } finally {
      setSavingRepo(null);
    }
  };

  return (
    <Box sx={{ mt: 3 }} data-testid="repo-connections">
      <Typography variant="subtitle2">Repositories</Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
        Which connection each of this deployment&apos;s repositories clones and
        pushes with: one chosen for the repository, else its owner&apos;s, else
        the host&apos;s default.
      </Typography>
      {repos.length === 0 ? (
        <Typography variant="body2" color="text.secondary">
          This deployment names no target repositories yet. Add them on the
          Repos pane to choose a connection for one.
        </Typography>
      ) : (
        <Stack spacing={1}>
          {repos.map((repo) => {
            const { host } = splitRepo(repo);
            const resolved = loading
              ? null
              : resolveRepoConnection(connections, repo);
            const pinned = repoPinnedConnection(connections, repo);
            const options = distinctConnections(
              connections.filter(
                (c) => (c.host || "github.com").toLowerCase() === host,
              ),
            );
            return (
              <Stack
                key={repo}
                direction="row"
                spacing={2}
                alignItems="center"
                data-testid={`repo-connection-${repo}`}
              >
                <Box sx={{ flex: 1, minWidth: 0 }}>
                  <Typography variant="body2">
                    <code>{repo}</code>
                  </Typography>
                  {loading ? null : resolved ? (
                    <Typography
                      variant="caption"
                      color="text.secondary"
                      data-testid={`repo-connection-resolved-${repo}`}
                    >
                      Uses {connectionLabel(resolved.connection)},{" "}
                      {MATCH_LABELS[resolved.match]}
                    </Typography>
                  ) : (
                    <Typography
                      variant="caption"
                      color="error"
                      display="block"
                      data-testid={`repo-connection-none-${repo}`}
                    >
                      No connection reaches this repository: its tasks are held
                      until one does.
                    </Typography>
                  )}
                </Box>
                <TextField
                  select
                  size="small"
                  label="Connection"
                  name={`repo-connection-picker-${repo}`}
                  value={pinned?.credential || ""}
                  disabled={readOnly || loading || savingRepo === repo}
                  onChange={(evt) => pick(repo, evt.target.value)}
                  sx={{ minWidth: 260 }}
                  SelectProps={{ displayEmpty: true }}
                  InputLabelProps={{ shrink: true }}
                >
                  <MenuItem value="">Default</MenuItem>
                  {options.map((conn) => (
                    <MenuItem key={conn.credential} value={conn.credential}>
                      {connectionLabel(conn)}
                    </MenuItem>
                  ))}
                </TextField>
              </Stack>
            );
          })}
        </Stack>
      )}
    </Box>
  );
}
