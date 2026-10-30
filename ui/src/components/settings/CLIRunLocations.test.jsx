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

import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../api.js", () => ({ default: vi.fn() }));

import api from "../../api.js";
import CLIRunLocations, {
  HostdUpgrade,
  locationsFor,
  processLine,
  resolvedLine,
} from "./CLIRunLocations.jsx";

// A development daemon on the bare machine: every CLI on the host, and
// a container stored by an older release shown with its warning.
const local = {
  kind: "local",
  hostAvailable: true,
  reason: "the daemon runs on this machine",
};
const hostClaude = {
  framework: "claude",
  setting: "host",
  source: "default",
  location: "host",
  via: "daemon",
  locations: ["host"],
  hostPath: "/usr/local/bin/claude",
  reason: "host the default",
};
const agy = {
  framework: "antigravity",
  setting: "host",
  source: "default",
  location: "host",
  via: "daemon",
  locations: ["host"],
  hostPath: "/opt/agy/bin/agy",
  reason: "host the default",
};

describe("CLIRunLocations on the bare machine", () => {
  beforeEach(() => {
    api.mockReset();
    api.mockImplementation((path, options) => {
      if (path === "/api/settings/cli-run" && !options)
        return Promise.resolve({
          settings: { claude: { location: "container" } },
          hostExecutor: local,
          resolved: [
            {
              ...hostClaude,
              setting: "container",
              source: "settings",
              warning:
                "set to container, but a daemon on the bare machine has no container of its own to run it in. It runs on the host until this is set to host or default",
            },
            agy,
          ],
        });
      if (path === "/api/settings/cli-run")
        return Promise.resolve({
          settings: JSON.parse(options.body).settings,
          hostExecutor: local,
          resolved: [{ ...hostClaude, source: "settings" }, agy],
        });
      return Promise.resolve({});
    });
  });

  it("offers only host, and clears a stored container", async () => {
    const user = userEvent.setup();
    const onSaved = vi.fn();
    render(<CLIRunLocations showError={vi.fn()} onSaved={onSaved} />);

    const claude = await screen.findByTestId("cli-run-claude");
    expect(
      within(claude).getByTestId("cli-run-claude-resolved"),
    ).toHaveTextContent("host (/usr/local/bin/claude)");
    expect(screen.getByTestId("cli-run-claude-warning")).toHaveTextContent(
      "set to container, but a daemon on the bare machine",
    );
    expect(screen.getByTestId("cli-run-locations")).toHaveTextContent(
      "This daemon runs on the bare machine",
    );

    await user.click(within(claude).getByLabelText("Runs on"));
    const options = screen.getAllByRole("option").map((o) => o.textContent);
    expect(options).toEqual([
      "Default",
      "Host",
      "Container (not available here)",
    ]);
    await user.click(screen.getByRole("option", { name: "Host" }));
    await user.click(
      screen.getByRole("button", { name: "Save run locations" }),
    );

    expect(api).toHaveBeenCalledWith("/api/settings/cli-run", {
      method: "PUT",
      body: JSON.stringify({ settings: { claude: { location: "host" } } }),
    });
    await waitFor(() =>
      expect(screen.queryByTestId("cli-run-claude-warning")).toBeNull(),
    );
    expect(onSaved).toHaveBeenCalled();
  });

  it("offers agy only the host", async () => {
    const user = userEvent.setup();
    render(<CLIRunLocations showError={vi.fn()} />);
    const row = await screen.findByTestId("cli-run-antigravity");
    await user.click(within(row).getByLabelText("Runs on"));
    const options = screen.getAllByRole("option").map((o) => o.textContent);
    expect(options).toEqual(["Default", "Host"]);
  });

  it("says a host resolution by its binary", () => {
    expect(resolvedLine(agy)).toBe("host (/opt/agy/bin/agy)");
  });
});

describe("CLIRunLocations with no host executor", () => {
  const k8s = {
    kind: "none",
    hostAvailable: false,
    reason:
      "this deployment has no host executor (Kubernetes): every agent CLI runs in the daemon pod's own container",
  };
  const inPod = (framework, extra = {}) => ({
    framework,
    setting: "container",
    source: "default",
    location: "container",
    via: "daemon",
    locations: ["container"],
    reason: "container the default: a subprocess in the daemon's own container",
    ...extra,
  });

  beforeEach(() => {
    api.mockReset();
    api.mockResolvedValue({
      settings: { claude: { location: "host" } },
      hostExecutor: k8s,
      resolved: [
        inPod("claude", {
          setting: "host",
          source: "settings",
          warning:
            "set to host, but this deployment has no host executor (Kubernetes). It runs in the container until this is set to container or default",
        }),
        inPod("codex", {
          upgradeInterrupts:
            "A daemon upgrade interrupts codex runs: codex runs in the daemon's own container, so the restart finishes each run in flight as interrupted and queues its task again.",
        }),
        inPod("antigravity"),
        inPod("custom"),
      ],
    });
  });

  it("says which CLIs a daemon upgrade interrupts", async () => {
    render(<CLIRunLocations showError={vi.fn()} />);
    expect(
      await screen.findByTestId("cli-run-codex-upgrade-interrupts"),
    ).toHaveTextContent("A daemon upgrade interrupts codex runs");
    expect(
      screen.queryByTestId("cli-run-antigravity-upgrade-interrupts"),
    ).toBeNull();
  });

  it("says why host is not offered and offers it for no CLI", async () => {
    const user = userEvent.setup();
    render(<CLIRunLocations showError={vi.fn()} />);

    expect(await screen.findByTestId("cli-run-no-host")).toHaveTextContent(
      "no host executor (Kubernetes)",
    );
    expect(screen.getByTestId("cli-run-codex-resolved")).toHaveTextContent(
      "container (the daemon's own)",
    );
    expect(screen.getByTestId("cli-run-claude-warning")).toHaveTextContent(
      "set to host, but this deployment has no host executor",
    );
    await user.click(
      within(screen.getByTestId("cli-run-codex")).getByLabelText("Runs on"),
    );
    const options = screen.getAllByRole("option").map((o) => o.textContent);
    expect(options).toEqual(["Default", "Container"]);
  });
});

describe("CLIRunLocations behind grain hostd", () => {
  const behindHostd = (framework, extra = {}) => ({
    framework,
    setting: "container",
    source: "default",
    location: "container",
    via: "daemon",
    locations: ["host", "container"],
    reason: "container the default: a subprocess in the daemon's own container",
    ...extra,
  });

  beforeEach(() => {
    api.mockReset();
    api.mockResolvedValue({
      settings: {},
      hostExecutor: {
        kind: "hostd",
        hostAvailable: true,
        socket: "/run/grain-hostd/hostd.sock",
        hostd: {
          reachable: true,
          offers: ["cli agy", "capability corp-search"],
        },
      },
      resolved: [
        behindHostd("claude", {
          hostUnavailable:
            'grain hostd answers, but its allowlist (/etc/grain/hostd.yaml) offers no cli entry "claude"',
        }),
        behindHostd("antigravity", {
          setting: "host",
          location: "host",
          via: "hostd",
          hostPath: "/opt/fakeagy/agy",
          reason: "host the default: through grain hostd",
        }),
      ],
    });
  });

  it("says what hostd offers, and why host is unavailable for a CLI it does not", async () => {
    render(<CLIRunLocations showError={vi.fn()} />);
    expect(await screen.findByTestId("cli-run-hostd-status")).toHaveTextContent(
      "offering cli agy, capability corp-search",
    );
    expect(
      screen.getByTestId("cli-run-claude-host-unavailable"),
    ).toHaveTextContent(
      'Host is unavailable for claude: grain hostd answers, but its allowlist (/etc/grain/hostd.yaml) offers no cli entry "claude"',
    );
    expect(
      screen.queryByTestId("cli-run-antigravity-host-unavailable"),
    ).toBeNull();
    expect(
      screen.getByTestId("cli-run-antigravity-resolved"),
    ).toHaveTextContent("host (/opt/fakeagy/agy, through grain hostd)");
  });

  it("says when hostd is updating, and when it speaks a protocol this daemon cannot", async () => {
    const withHostd = (hostd) => ({
      settings: {},
      hostExecutor: {
        kind: "hostd",
        hostAvailable: true,
        socket: "/run/grain-hostd/hostd.sock",
        hostd: { reachable: true, offers: ["cli agy"], protocol: 2, ...hostd },
      },
      resolved: [],
    });
    api.mockResolvedValue(withHostd({ updating: true }));
    const { unmount } = render(<CLIRunLocations showError={vi.fn()} />);
    expect(await screen.findByTestId("cli-run-hostd-status")).toHaveTextContent(
      "it is updating: new host runs wait until it is back",
    );
    unmount();

    api.mockResolvedValue(
      withHostd({
        protocol: 9,
        mismatch: "hostd speaks protocol 9 (needs at least 8)",
      }),
    );
    render(<CLIRunLocations showError={vi.fn()} />);
    expect(await screen.findByTestId("cli-run-hostd-status")).toHaveTextContent(
      "cannot run this daemon's host CLIs (hostd speaks protocol 9 (needs at least 8))",
    );
  });
});

describe("a grain hostd that dials in", () => {
  const remoteHostd = (hostd) => ({
    settings: {},
    hostExecutor: {
      kind: "hostd",
      hostAvailable: true,
      remote: true,
      reason: "host CLIs run through a grain hostd that dials in",
      hostd,
    },
    resolved: [],
  });

  it("names the host it connected from and what it offers", async () => {
    api.mockReset();
    api.mockResolvedValue(
      remoteHostd({
        reachable: true,
        offers: ["cli agy", "capability corp-search"],
        protocol: 3,
        remote: { connected: true, host: "workstation", sessions: 1 },
      }),
    );
    render(<CLIRunLocations showError={vi.fn()} />);
    const status = await screen.findByTestId("cli-run-hostd-status");
    expect(status).toHaveTextContent(
      "grain hostd is up on workstation, connected to this daemon, offering cli agy, capability corp-search. Host runs go through it.",
    );
    expect(status).not.toHaveTextContent("at undefined");
  });

  it("says none is connected, and how to connect one", async () => {
    api.mockReset();
    api.mockResolvedValue(
      remoteHostd({
        reachable: false,
        error:
          "grain hostd is not reachable: no grain hostd is connected to this daemon",
        remote: { connected: false, sessions: 0 },
      }),
    );
    render(<CLIRunLocations showError={vi.fn()} />);
    const status = await screen.findByTestId("cli-run-hostd-status");
    expect(status).toHaveTextContent(
      "This daemon takes a grain hostd that dials in to it, but none is connected. A CLI set to host fails its runs until one does.",
    );
    expect(status).toHaveTextContent(
      "this daemon logs a hostd it refuses for its token.",
    );
    // An installed grain-hostd.service takes the flags in a drop-in, not
    // a second hostd beside it sharing its state directory.
    expect(status).toHaveTextContent(
      "rather than starting a second hostd beside it (the two would share one state directory). Run sudo systemctl edit grain-hostd.service",
    );
    // The browser's origin may be a proxy hostd cannot reach, so the URL
    // stays a placeholder.
    expect(screen.getByTestId("cli-run-hostd-dropin").textContent).toBe(
      "[Service]\nExecStart=\nExecStart=<the unit's ExecStart> -server <this daemon's URL> -token-file <the hostd token>",
    );
    expect(status).toHaveTextContent(
      "Then run sudo systemctl restart grain-hostd.service.",
    );
    // A hand-run hostd works without the installer's /run/grain-hostd
    // and /etc/grain/hostd.yaml.
    expect(screen.getByTestId("cli-run-hostd-command").textContent).toBe(
      "grain hostd -server <this daemon's URL> -token-file <the hostd token> -socket= -allowlist <file>",
    );
  });
});

describe("hostd's build and the processes it holds", () => {
  it("names hostd's build and lists each detached run", async () => {
    api.mockReset();
    api.mockResolvedValue({
      settings: {},
      hostExecutor: {
        kind: "hostd",
        hostAvailable: true,
        socket: "/run/grain-hostd/hostd.sock",
        hostd: {
          reachable: true,
          offers: ["cli agy"],
          protocol: 2,
          build: "v2.3.0-dev.168+wt5047",
          processes: [
            {
              id: "p1",
              run: "9-1",
              task: "9",
              kind: "cli",
              name: "agy",
              pid: 17616,
              detached: true,
            },
            {
              id: "p2",
              kind: "capability",
              name: "fakecap",
              pid: 16551,
              attached: true,
            },
          ],
        },
      },
      resolved: [],
    });
    render(<CLIRunLocations showError={vi.fn()} />);
    expect(await screen.findByTestId("cli-run-hostd-status")).toHaveTextContent(
      "(build v2.3.0-dev.168+wt5047, hostd protocol 2), offering cli agy",
    );
    const procs = screen.getByTestId("cli-run-hostd-processes");
    expect(procs).toHaveTextContent("grain hostd is holding 2 processes");
    expect(procs).toHaveTextContent(
      "agy for run 9-1 of task 9: pid 17616, running, no daemon attached",
    );
    expect(procs).toHaveTextContent("capability fakecap: pid 16551, running");
  });
});

describe("locationsFor", () => {
  it("keeps a stored location the daemon no longer accepts, disabled", () => {
    const got = locationsFor({ locations: ["container"] }, "host");
    expect(got.map((l) => [l.value, l.disabled])).toEqual([
      ["", false],
      ["host", true],
      ["container", false],
    ]);
  });

  it("offers both with no daemon to ask", () => {
    expect(locationsFor(undefined, "").map((l) => l.value)).toEqual([
      "",
      "host",
      "container",
    ]);
  });
});

describe("resolvedLine through a containerised daemon", () => {
  it("names the daemon's own container and grain hostd", () => {
    expect(
      resolvedLine({
        framework: "codex",
        location: "container",
        via: "daemon",
      }),
    ).toBe("container (the daemon's own)");
    expect(
      resolvedLine({
        framework: "antigravity",
        location: "host",
        via: "hostd",
      }),
    ).toBe("host (through grain hostd)");
    expect(
      resolvedLine({
        framework: "antigravity",
        location: "host",
        via: "hostd",
        hostPath: "/opt/agy/bin/agy",
      }),
    ).toBe("host (/opt/agy/bin/agy, through grain hostd)");
  });
});

describe("the detached location", () => {
  it("is offered where the daemon accepts it, and named by its entry", () => {
    const got = locationsFor(
      { locations: ["host", "container", "detached"] },
      "",
    );
    expect(got.map((l) => [l.value, l.disabled])).toEqual([
      ["", false],
      ["host", false],
      ["container", false],
      ["detached", false],
    ]);
    // Not offered with no daemon to say so, nor for a CLI without it.
    expect(locationsFor(undefined, "").map((l) => l.value)).not.toContain(
      "detached",
    );
    expect(
      resolvedLine({
        framework: "claude",
        location: "detached",
        via: "hostd",
        entry: "claude-detached",
        hostPath: "/usr/local/lib/grain/hostd/current/claude",
      }),
    ).toBe(
      "detached (/usr/local/lib/grain/hostd/current/claude, through grain hostd as claude-detached; outlives a daemon upgrade)",
    );
  });
});

describe("HostdUpgrade", () => {
  it("names the installed build while hostd drains onto it, and suggests a missing entry", () => {
    render(
      <HostdUpgrade
        hostd={{
          reachable: true,
          updating: true,
          running: "aaaa1111bbbb",
          current: "dddd3333eeee",
          currentBuild: "v2.2.0",
          suggest: ["cli agy"],
        }}
      />,
    );
    expect(screen.getByTestId("cli-run-hostd-current")).toHaveTextContent(
      "Installed: build v2.2.0 (version dddd3333eeee); grain hostd runs version aaaa1111bbbb and restarts on the installed one once the runs it holds have ended.",
    );
    expect(screen.getByTestId("cli-run-hostd-suggest")).toHaveTextContent(
      "Suggested allowlist entry: cli agy",
    );
  });

  it("says nothing when hostd runs what is installed and the allowlist lacks nothing", () => {
    const { container } = render(
      <HostdUpgrade
        hostd={{ reachable: true, running: "aaaa", current: "aaaa" }}
      />,
    );
    expect(container).toBeEmptyDOMElement();
  });

  it("marks a run this daemon adopted", () => {
    expect(
      processLine({
        kind: "cli",
        name: "agy",
        run: "9-1",
        task: "9",
        pid: 7,
        detached: true,
        attached: true,
        adopted: true,
      }),
    ).toBe(
      "agy for run 9-1 of task 9: pid 7, running, detached, adopted by this daemon",
    );
  });
});
