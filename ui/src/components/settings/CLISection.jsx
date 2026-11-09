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

import { Alert, Box, Link, Stack, Typography } from "@mui/material";
import SettingsCard from "./SettingsCard.jsx";
import CopyButton from "../CopyButton.jsx";

// SKILL_AGENTS are the agents `grain skill install` writes a SKILL.md
// for (pkg/cli/skill.go), each by its own flag.
export const SKILL_AGENTS = [
  { flag: "--jetski", label: "Jetski" },
  { flag: "--claude", label: "Claude Code" },
  { flag: "--codex", label: "Codex" },
  { flag: "--gemini", label: "Gemini CLI" },
];

// contextSlug is the context name `grain login` gives a deployment when
// --name is left out (pkg/cli/login.go contextSlug): its hostname,
// lowercased, with every run of other characters made one hyphen.
export function contextSlug(host) {
  const s = String(host || "")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
  return s || "default";
}

// cliCommands are the lines Settings -> CLI shows for a deployment at
// origin: the install line, the login line and one skill line per agent.
export function cliCommands(origin) {
  let host = "";
  try {
    host = new URL(origin).hostname;
  } catch {
    // An origin that is not a URL names no host; the slug is "default".
  }
  return {
    install: `curl -fsSL ${origin}/cli/install.sh | sh`,
    login: `grain login ${origin} --name ${contextSlug(host)} --issue-token --scope-set grain/external-dev --expires 30d`,
    skills: SKILL_AGENTS.map((a) => ({
      ...a,
      command: `grain skill install ${a.flag}`,
    })),
  };
}

// Code is a flag or path inline in prose, kept on one line so
// "--repos" never breaks at its hyphens.
function Code({ children }) {
  return (
    <Box component="code" sx={{ whiteSpace: "nowrap" }}>
      {children}
    </Box>
  );
}

function Command({ text, testId, label }) {
  return (
    <Stack
      direction="row"
      alignItems="flex-start"
      spacing={0.5}
      sx={{ mt: 0.5 }}
    >
      <Box
        component="pre"
        data-testid={testId}
        sx={{
          flex: 1,
          minWidth: 0,
          m: 0,
          p: 1,
          borderRadius: 1,
          bgcolor: "action.hover",
          fontFamily: "monospace",
          fontSize: "0.85em",
          whiteSpace: "pre-wrap",
          // Wrap at the spaces between arguments, and inside one only
          // when it is wider than the box on its own.
          overflowWrap: "anywhere",
        }}
      >
        {text}
      </Box>
      <CopyButton text={text} label={label} />
    </Stack>
  );
}

// CLISection is Settings → CLI (external-cli.spec.md#XC-32): how to put
// the grain CLI on a workstation, sign it in to this deployment and hand
// it to a coding agent. It saves nothing; the one setting it depends on,
// externalAgentApi, is Settings → Agents'.
export default function CLISection({ s, config, onSection }) {
  const origin = window.location.origin;
  const cmds = cliCommands(origin);
  const release = config?.version?.unversioned
    ? ""
    : config?.version?.release || "";
  const externalOn = !!s?.settings?.externalAgentApi;
  return (
    <>
      <SettingsCard title="Install" anchor="cli-install" allowInExternalMode>
        <Typography variant="body2" color="text.secondary">
          Run this on your workstation (Linux or macOS, amd64 or arm64). It
          downloads{" "}
          {release ? <Code>grain {release}</Code> : "the grain release"} this
          deployment runs, checks its SHA-256 against the release&apos;s
          checksums before unpacking it, and installs <Code>grain</Code> to{" "}
          <Code>~/.local/bin</Code> (set <Code>GRAIN_INSTALL_DIR</Code> for
          somewhere else).
        </Typography>
        <Command
          text={cmds.install}
          testId="cli-install-command"
          label="Copy install command"
        />
      </SettingsCard>
      <SettingsCard title="Sign in" anchor="cli-login" allowInExternalMode>
        {!externalOn && (
          <Alert
            severity="warning"
            sx={{ mt: 1 }}
            data-testid="cli-external-agent-api-off"
          >
            The external agent API is off on this deployment, so{" "}
            <Code>--issue-token</Code> has no token to mint yet. Turn it on in{" "}
            <Link
              href="/settings/agents#external-agents"
              data-testid="cli-external-agent-api-link"
              onClick={(evt) => {
                if (
                  !onSection ||
                  evt.button !== 0 ||
                  evt.metaKey ||
                  evt.ctrlKey ||
                  evt.shiftKey ||
                  evt.altKey
                )
                  return;
                evt.preventDefault();
                onSection("agents", "external-agents");
              }}
            >
              Settings &rarr; Agents
            </Link>{" "}
            first.
          </Alert>
        )}
        <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>
          Signs <Code>grain</Code> in to this deployment as you and keeps a
          30-day token scoped to <Code>grain/external-dev</Code> for it. Add{" "}
          <Code>--repos owner/name,...</Code> to limit the token to some
          repositories.
        </Typography>
        <Command
          text={cmds.login}
          testId="cli-login-command"
          label="Copy login command"
        />
      </SettingsCard>
      <SettingsCard
        title="Agent skills"
        anchor="cli-skills"
        allowInExternalMode
      >
        <Typography variant="body2" color="text.secondary">
          Teaches a coding agent on your workstation to work through{" "}
          <Code>grain</Code>: each writes a <Code>SKILL.md</Code> for the
          signed-in deployment where that agent reads skills. Add{" "}
          <Code>--project</Code> to write it into the current repository
          instead, and <Code>--mcp</Code> to register <Code>grain mcp</Code>{" "}
          too.
        </Typography>
        {cmds.skills.map((a) => (
          <Box key={a.flag} sx={{ mt: 1 }}>
            <Typography variant="body2">{a.label}</Typography>
            <Command
              text={a.command}
              testId={`cli-skill-command-${a.flag.slice(2)}`}
              label={`Copy ${a.label} skill command`}
            />
          </Box>
        ))}
      </SettingsCard>
    </>
  );
}
