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

import { useState, useEffect, useCallback } from "react";
import {
  Alert,
  Box,
  Button,
  Card,
  CardContent,
  Chip,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import AddIcon from "@mui/icons-material/Add";
import ContentCopyIcon from "@mui/icons-material/ContentCopy";
import BlockIcon from "@mui/icons-material/Block";
import CheckCircleOutlineIcon from "@mui/icons-material/CheckCircleOutline";
import api from "../api.js";
import { useTimeZone } from "../TimeZoneContext.jsx";
import { formatDateTime } from "../time.js";

// Every executor-token endpoint is deployment-admin only, so canManage says whether the caller is one. Without it the
// panel explains who manages tokens instead of listing an empty set and
// offering an Issue Token that can only be refused.
export default function ExecutorsPanel({ showError, canManage = true }) {
  const timeZone = useTimeZone();
  const [tokens, setTokens] = useState([]);
  const [loading, setLoading] = useState(false);
  // Set when the server refuses the list even though canManage said yes
  // (a stale config, an admin list changed underneath): the same
  // explanation, not a toast over a false empty state.
  const [forbidden, setForbidden] = useState(false);
  const allowed = canManage && !forbidden;

  // Issue Dialog State
  const [issueOpen, setIssueOpen] = useState(false);
  const [tokenName, setTokenName] = useState("");
  const [issuing, setIssuing] = useState(false);

  // One-time Token Display State
  const [createdToken, setCreatedToken] = useState(null);
  const [tokenCopied, setTokenCopied] = useState(false);

  // Revoke Dialog State
  const [revokingToken, setRevokingToken] = useState(null);
  const [revoking, setRevoking] = useState(false);

  const fetchTokens = useCallback(async () => {
    setLoading(true);
    try {
      const data = await api("/api/executors/tokens");
      setTokens(Array.isArray(data) ? data : []);
    } catch (err) {
      if (err?.status === 403) {
        setTokens([]);
        setForbidden(true);
      } else {
        showError?.(err);
      }
    } finally {
      setLoading(false);
    }
  }, [showError]);

  useEffect(() => {
    if (canManage) fetchTokens();
  }, [canManage, fetchTokens]);

  const handleOpenIssue = () => {
    setTokenName("");
    setIssueOpen(true);
  };

  const handleIssueToken = async (e) => {
    e?.preventDefault();
    if (!tokenName.trim()) return;
    setIssuing(true);
    try {
      const res = await api("/api/executors/tokens", {
        method: "POST",
        body: JSON.stringify({ name: tokenName.trim() }),
      });
      setIssueOpen(false);
      setCreatedToken(res);
      setTokenCopied(false);
      await fetchTokens();
    } catch (err) {
      showError?.(err);
    } finally {
      setIssuing(false);
    }
  };

  const handleRevokeToken = async () => {
    if (!revokingToken) return;
    setRevoking(true);
    try {
      await api(
        `/api/executors/tokens/${encodeURIComponent(revokingToken.id)}/revoke`,
        {
          method: "POST",
        },
      );
      setRevokingToken(null);
      await fetchTokens();
    } catch (err) {
      showError?.(err);
    } finally {
      setRevoking(false);
    }
  };

  const copyToClipboard = (text) => {
    if (navigator?.clipboard?.writeText) {
      navigator.clipboard.writeText(text);
      setTokenCopied(true);
      setTimeout(() => setTokenCopied(false), 3000);
    }
  };

  const visibleTokens = tokens;

  return (
    <Box>
      <Stack
        direction={{ xs: "column", sm: "row" }}
        alignItems={{ xs: "stretch", sm: "center" }}
        justifyContent="space-between"
        spacing={1.5}
        sx={{ mb: 2 }}
      >
        <Box>
          <Typography variant="subtitle2">Model servers</Typography>
          <Typography variant="body2" color="text.secondary">
            Manage authentication credentials for remote model server worker
            daemons (<code>grain model-server</code>). Tokens can be revoked at
            any time to immediately disconnect running workers.
          </Typography>
        </Box>
        {allowed && (
          <Button
            variant="contained"
            startIcon={<AddIcon />}
            onClick={handleOpenIssue}
            sx={{ whiteSpace: "nowrap" }}
          >
            Issue Token
          </Button>
        )}
      </Stack>

      {!allowed && (
        <Alert severity="info" sx={{ mb: 2 }}>
          Executor tokens are managed by deployment admins. Ask one to issue or
          revoke a token.
        </Alert>
      )}

      {allowed && visibleTokens.length === 0 && !loading && (
        <Typography
          variant="body2"
          color="text.secondary"
          sx={{ py: 3, textAlign: "center" }}
        >
          No executor tokens found. Issue a token to connect remote worker
          machines.
        </Typography>
      )}

      <Stack spacing={1.5}>
        {visibleTokens.map((tok) => {
          const revokedAt = tok.revoked_at || tok.revokedAt;
          const createdAt = tok.created_at || tok.createdAt;
          const lastConnectedAt = tok.last_connected_at || tok.lastConnectedAt;
          const isRevoked = Boolean(revokedAt);
          return (
            <Card
              key={tok.id}
              variant="outlined"
              sx={{ opacity: isRevoked ? 0.7 : 1 }}
            >
              <CardContent sx={{ py: 1.5, px: 2, "&:last-child": { pb: 1.5 } }}>
                <Stack
                  direction={{ xs: "column", sm: "row" }}
                  alignItems={{ xs: "flex-start", sm: "center" }}
                  justifyContent="space-between"
                  spacing={1}
                >
                  <Box sx={{ minWidth: 0, maxWidth: "100%" }}>
                    {/* A name with nowhere to break must wrap inside the
                        card, not push the chips and Revoke off the side. */}
                    <Stack
                      direction="row"
                      alignItems="center"
                      spacing={1}
                      useFlexGap
                      sx={{ mb: 0.5, flexWrap: "wrap", rowGap: 0.5 }}
                    >
                      <Typography
                        variant="subtitle2"
                        sx={{
                          fontWeight: 600,
                          minWidth: 0,
                          overflowWrap: "anywhere",
                        }}
                      >
                        {tok.name}
                      </Typography>
                      <Chip
                        size="small"
                        label={isRevoked ? "Revoked" : "Active"}
                        color={isRevoked ? "default" : "success"}
                        variant={isRevoked ? "outlined" : "filled"}
                      />
                    </Stack>
                    <Stack
                      direction="row"
                      spacing={2}
                      sx={{
                        flexWrap: "wrap",
                        rowGap: 0.5,
                        color: "text.secondary",
                      }}
                    >
                      <Typography variant="caption">
                        Prefix: <code>{tok.prefix}…</code>
                      </Typography>
                      <Typography variant="caption">
                        Created:{" "}
                        {createdAt
                          ? formatDateTime(createdAt, timeZone)
                          : "Unknown"}
                      </Typography>
                      <Typography variant="caption">
                        Last Connected:{" "}
                        {lastConnectedAt
                          ? formatDateTime(lastConnectedAt, timeZone)
                          : "Never"}
                      </Typography>
                      {revokedAt && (
                        <Typography variant="caption" color="error">
                          Revoked: {formatDateTime(revokedAt, timeZone)}
                        </Typography>
                      )}
                    </Stack>
                  </Box>

                  {!isRevoked && (
                    <Button
                      size="small"
                      color="error"
                      variant="outlined"
                      startIcon={<BlockIcon />}
                      onClick={() => setRevokingToken(tok)}
                      sx={{ alignSelf: { xs: "flex-end", sm: "center" } }}
                    >
                      Revoke
                    </Button>
                  )}
                </Stack>
              </CardContent>
            </Card>
          );
        })}
      </Stack>

      {/* Issue Token Dialog */}
      <Dialog
        open={issueOpen}
        onClose={() => !issuing && setIssueOpen(false)}
        maxWidth="sm"
        fullWidth
      >
        <DialogTitle>Issue Executor Token</DialogTitle>
        <form onSubmit={handleIssueToken}>
          <DialogContent>
            <Stack spacing={2} sx={{ mt: 0.5 }}>
              <Typography variant="body2" color="text.secondary">
                Generate a secret token for a remote executor daemon. The token
                will be displayed once upon creation.
              </Typography>
              <TextField
                autoFocus
                label="Token Name / Description"
                placeholder="e.g. gcp-gpu-worker-1"
                value={tokenName}
                onChange={(e) => setTokenName(e.target.value)}
                inputProps={{ maxLength: 128 }}
                required
                fullWidth
                size="small"
              />
            </Stack>
          </DialogContent>
          <DialogActions>
            <Button onClick={() => setIssueOpen(false)} disabled={issuing}>
              Cancel
            </Button>
            <Button
              type="submit"
              variant="contained"
              disabled={issuing || !tokenName.trim()}
            >
              {issuing ? "Generating..." : "Generate Token"}
            </Button>
          </DialogActions>
        </form>
      </Dialog>

      {/* Token Created One-Time Display Dialog */}
      <Dialog
        open={Boolean(createdToken)}
        onClose={() => setCreatedToken(null)}
        maxWidth="md"
        fullWidth
      >
        <DialogTitle>Executor Token Generated</DialogTitle>
        <DialogContent>
          <Stack spacing={2} sx={{ mt: 0.5 }}>
            <Alert severity="warning">
              Make sure to copy this token now. It will <strong>never</strong>{" "}
              be shown again!
            </Alert>
            <Box>
              <Typography
                variant="caption"
                color="text.secondary"
                sx={{ display: "block", mb: 0.5, overflowWrap: "anywhere" }}
              >
                Token for: <strong>{createdToken?.record?.name}</strong>
              </Typography>
              <Stack direction="row" spacing={1} alignItems="center">
                <TextField
                  value={createdToken?.token || ""}
                  fullWidth
                  size="small"
                  InputProps={{
                    readOnly: true,
                    sx: { fontFamily: "monospace", fontSize: "0.9rem" },
                  }}
                />
                <Button
                  variant="outlined"
                  startIcon={
                    tokenCopied ? (
                      <CheckCircleOutlineIcon />
                    ) : (
                      <ContentCopyIcon />
                    )
                  }
                  color={tokenCopied ? "success" : "primary"}
                  onClick={() => copyToClipboard(createdToken?.token || "")}
                >
                  {tokenCopied ? "Copied" : "Copy"}
                </Button>
              </Stack>
            </Box>

            <Box sx={{ mt: 1 }}>
              <Typography variant="subtitle2" sx={{ mb: 0.5 }}>
                Connect a Model Server
              </Typography>
              <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
                Run the grain model-server daemon on your remote machine using
                this token:
              </Typography>
              <Box
                component="pre"
                sx={{
                  p: 1.5,
                  borderRadius: 1,
                  bgcolor: "grey.100",
                  color: "grey.900",
                  fontFamily: "monospace",
                  fontSize: "0.85rem",
                  overflowX: "auto",
                  m: 0,
                }}
              >
                {`grain model-server --token ${createdToken?.token || "<token>"} --server <server-addr:port>`}
              </Box>
            </Box>
          </Stack>
        </DialogContent>
        <DialogActions>
          <Button variant="contained" onClick={() => setCreatedToken(null)}>
            Done
          </Button>
        </DialogActions>
      </Dialog>

      {/* Revoke Confirmation Dialog */}
      <Dialog
        open={Boolean(revokingToken)}
        onClose={() => !revoking && setRevokingToken(null)}
      >
        <DialogTitle>Revoke Executor Token</DialogTitle>
        <DialogContent>
          <Typography
            variant="body2"
            sx={{ mb: 1.5, overflowWrap: "anywhere" }}
          >
            Are you sure you want to revoke token{" "}
            <strong>{revokingToken?.name}</strong> (Prefix:{" "}
            <code>{revokingToken?.prefix}…</code>)?
          </Typography>
          <Alert severity="error">
            Any active executor connected using this token will be disconnected
            immediately and blocked from reconnecting.
          </Alert>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setRevokingToken(null)} disabled={revoking}>
            Cancel
          </Button>
          <Button
            color="error"
            variant="contained"
            onClick={handleRevokeToken}
            disabled={revoking}
          >
            {revoking ? "Revoking..." : "Revoke Token"}
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  );
}
