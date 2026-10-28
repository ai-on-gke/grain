import { useCallback, useEffect, useState } from "react";
import { Alert, Box, Button, Chip, Stack, Typography } from "@mui/material";
import api from "../api.js";
import { useTimeZone } from "../TimeZoneContext.jsx";
import { formatDateTime } from "../time.js";

// GChatSpaceList is a gchat server's configured spaces (docs/gchat.md)
// on its Settings card: each one's transport and usage -- posted, refused
// and failed since the server started, and this hour against its cap --
// and a Send test message button. The usage is
// GET /api/capability-servers/gchat-usage (the server's usage tool); the
// button is POST /api/capability-servers/gchat-test (its test_post tool),
// which posts one message marked [grain test] to the space as the server
// has it configured now -- the saved configuration, not an unsaved form
// -- and says the message it created, or why Chat refused it. Spaces and
// apps themselves are added through Configure.
export const GCHAT_TEST_TOOL = "test_post";

// isGChatServer is the built-in gchat server, or any server whose attach
// status advertises gchat's test_post control-plane tool -- an
// operator's own entry running the same server.
export function isGChatServer(server) {
  return (
    server?.builtin?.name === "gchat" ||
    Boolean(server?.status?.tools?.includes(GCHAT_TEST_TOOL))
  );
}

export default function GChatSpaceList({ server, readOnly }) {
  const spaces = Array.isArray(server.config?.config?.spaces)
    ? server.config.config.spaces
    : [];
  const [usage, setUsage] = useState({});
  const [usageError, setUsageError] = useState("");
  // Only an attached server answers its tools: one that has not
  // attached the saved configuration yet lists the spaces without usage.
  const attached = Boolean(server.status?.tools?.includes(GCHAT_TEST_TOOL));
  const spacesKey = spaces.map((s) => s.name).join(",");
  const load = useCallback(async () => {
    try {
      const rows = await api(
        `/api/capability-servers/gchat-usage?id=${encodeURIComponent(server.id)}`,
      );
      setUsage(
        Object.fromEntries(
          (Array.isArray(rows) ? rows : []).map((u) => [u.space, u]),
        ),
      );
      setUsageError("");
    } catch (err) {
      setUsageError(err.message);
    }
  }, [server.id]);
  useEffect(() => {
    if (attached && spacesKey) load();
  }, [load, attached, spacesKey]);
  if (spaces.length === 0) {
    return (
      <Typography
        variant="body2"
        color="text.secondary"
        sx={{ mt: 1 }}
        data-testid="gchat-space-list"
      >
        No spaces yet: Configure adds them.
      </Typography>
    );
  }
  return (
    <Stack spacing={1} sx={{ mt: 1 }} data-testid="gchat-space-list">
      <Typography variant="body2" sx={{ fontWeight: 600 }}>
        Spaces
      </Typography>
      {usageError && (
        <Alert severity="warning" sx={{ overflowWrap: "anywhere" }}>
          Usage unavailable: {usageError}
        </Alert>
      )}
      {spaces.map((s, i) => (
        <GChatSpaceRow
          key={`${s.name}-${i}`}
          serverID={server.id}
          space={s}
          usage={usage[s.name]}
          readOnly={readOnly || !attached}
          onPosted={load}
        />
      ))}
    </Stack>
  );
}

function usageLabels(u, perHourDefault) {
  if (!u) return [];
  const cap = u.per_hour || perHourDefault;
  return [
    `${u.posted} posted`,
    `${u.refused} refused`,
    `${u.failed} failed`,
    cap ? `${u.last_hour} / ${cap} this hour` : `${u.last_hour} this hour`,
  ];
}

function GChatSpaceRow({ serverID, space, usage, readOnly, onPosted }) {
  const zone = useTimeZone();
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState(null);
  const send = async () => {
    setBusy(true);
    setResult(null);
    try {
      setResult(
        await api("/api/capability-servers/gchat-test", {
          method: "POST",
          body: JSON.stringify({ id: serverID, space: space.name }),
        }),
      );
    } catch (err) {
      setResult({ error: err.message });
    } finally {
      setBusy(false);
      onPosted();
    }
  };
  const where =
    space.transport === "app"
      ? [space.space, space.app && `as ${space.app}`].filter(Boolean).join(" ")
      : "incoming webhook";
  return (
    <Box
      data-gchat-space={space.name}
      sx={{
        border: 1,
        borderColor: "divider",
        borderRadius: 1,
        px: 1.5,
        py: 1,
        minWidth: 0,
      }}
    >
      <Stack
        direction={{ xs: "column", sm: "row" }}
        spacing={1}
        alignItems={{ xs: "flex-start", sm: "center" }}
        justifyContent="space-between"
      >
        <Box sx={{ minWidth: 0, overflowWrap: "anywhere" }}>
          <Typography
            variant="body2"
            component="h5"
            sx={{ fontFamily: "monospace", fontWeight: 600 }}
          >
            {space.name}
          </Typography>
          <Typography variant="body2" color="text.secondary">
            {space.transport} · {where}
            {space.display_name && ` · posts as “${space.display_name}”`}
          </Typography>
        </Box>
        <Button
          size="small"
          variant="outlined"
          disabled={readOnly || busy}
          onClick={send}
          sx={{ flexShrink: 0 }}
        >
          {busy ? "Sending…" : "Send test message"}
        </Button>
      </Stack>
      <Stack
        direction="row"
        sx={{ mt: 1, flexWrap: "wrap", gap: 0.5 }}
        data-testid="gchat-space-usage"
      >
        {usageLabels(usage, space.allowed?.per_hour).map((l) => (
          <Chip key={l} size="small" variant="outlined" label={l} />
        ))}
        {usage?.last_post && (
          <Chip
            size="small"
            variant="outlined"
            label={`last post ${formatDateTime(usage.last_post, zone)}`}
          />
        )}
      </Stack>
      {result && (
        <Alert
          severity={result.error ? "error" : "success"}
          sx={{ mt: 1, overflowWrap: "anywhere" }}
          data-testid="gchat-test-result"
        >
          {result.error ? (
            <>Not posted: {result.error}</>
          ) : (
            <>
              Posted <code>{result.message}</code>
              {result.thread && (
                <>
                  {" "}
                  in thread <code>{result.thread}</code>
                </>
              )}
              .
            </>
          )}
        </Alert>
      )}
    </Box>
  );
}
