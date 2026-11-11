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

import { fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import ConnectionHostdField, {
  hostdLogins,
  hostdOption,
  missingLogin,
} from "./ConnectionHostdField.jsx";
import api from "../../api.js";

vi.mock("../../api.js", () => ({ default: vi.fn() }));

const registry = [
  { name: "local", owner: "" },
  {
    name: "ws-a",
    owner: "dana@example.com",
    connection: { connected: true, principal: "dana", host: "build-box" },
  },
  { name: "ws-off", owner: "", connection: { connected: false } },
  { name: "gone", revoked_at: "2026-11-01T00:00:00Z" },
];

describe("ConnectionHostdField", () => {
  afterEach(() => api.mockReset());

  it("lists the registry with owner, host user and connection, and picks one", async () => {
    api.mockResolvedValue(registry);
    const onChange = vi.fn();
    render(<ConnectionHostdField backend={{}} onChange={onChange} />);
    fireEvent.mouseDown(
      await screen.findByRole("combobox", { name: "Run on host" }),
    );
    const list = within(await screen.findByRole("listbox"));
    await list.findByText(/host ws-a/);
    expect(list.getByText(/host ws-a/).textContent).toBe(
      "host ws-a · connected · owner dana@example.com · runs as dana · on build-box",
    );
    expect(list.getByText(/host ws-off/).textContent).toContain(
      "not connected",
    );
    expect(list.queryByText(/host gone/)).toBeNull();
    fireEvent.click(list.getByText(/host ws-a/));
    expect(onChange).toHaveBeenCalledWith({ hostd: "ws-a" });
  });

  it("offers a fallback list only once a hostd is picked, empty by default", async () => {
    api.mockResolvedValue(registry);
    const onChange = vi.fn();
    const { rerender } = render(
      <ConnectionHostdField backend={{}} onChange={onChange} />,
    );
    await screen.findByRole("combobox", { name: "Run on host" });
    expect(screen.queryByTestId("connection-hostd-fallback")).toBeNull();
    rerender(
      <ConnectionHostdField backend={{ hostd: "ws-a" }} onChange={onChange} />,
    );
    const fallback = screen.getByTestId("connection-hostd-fallback");
    expect(fallback).toHaveValue("");
    fireEvent.change(fallback, { target: { value: "ws-off; pool=spare" } });
    expect(onChange).toHaveBeenLastCalledWith({
      hostdFallback: ["ws-off", "pool=spare"],
    });
  });

  // 2026-11-11 walkthrough: on a connection's page, "A host selected by
  // label…" then an ambient host picked under Credentials saved that host,
  // but Run on host went on saying "by label" over an empty label box.
  it("leaves label mode when a named host is set from outside", async () => {
    api.mockResolvedValue(registry);
    const onChange = vi.fn();
    const { rerender } = render(
      <ConnectionHostdField backend={{}} onChange={onChange} />,
    );
    fireEvent.mouseDown(
      await screen.findByRole("combobox", { name: "Run on host" }),
    );
    fireEvent.click(
      within(await screen.findByRole("listbox")).getByText(
        "A host selected by label…",
      ),
    );
    expect(screen.getByTestId("connection-hostd-labels")).toBeInTheDocument();
    rerender(
      <ConnectionHostdField backend={{ hostd: "ws-a" }} onChange={onChange} />,
    );
    expect(
      screen.getByRole("combobox", { name: "Run on host" }).textContent,
    ).toMatch(/^host ws-a/);
    expect(screen.queryByTestId("connection-hostd-labels")).toBeNull();
  });

  it("takes a label selector, and has no fallback for a capability server", async () => {
    api.mockResolvedValue(registry);
    const onChange = vi.fn();
    render(
      <ConnectionHostdField
        backend={{ hostd: "pool=gpu" }}
        onChange={onChange}
        noFallback
        testId="capability-server-hostd"
      />,
    );
    const labels = await screen.findByTestId("capability-server-hostd-labels");
    expect(labels).toHaveValue("pool=gpu");
    expect(screen.queryByTestId("capability-server-hostd-fallback")).toBeNull();
  });

  it("says a hostd's line as the picker shows it", () => {
    expect(hostdOption({ name: "local" })).toBe("local · connected");
  });

  it("says which agent CLI logins a host reports, and which one it lacks", () => {
    expect(hostdLogins({ name: "ws-a" })).toBe("");
    expect(hostdLogins({ name: "ws-a", loginsKnown: true })).toBe(
      "no CLI logins",
    );
    const wsA = {
      name: "ws-a",
      connection: { connected: true },
      logins: ["claude", "codex"],
      loginsKnown: true,
    };
    expect(hostdOption(wsA)).toBe("ws-a · connected · logins: claude, codex");
    const ambient = (framework) => ({ framework, authMode: "ambient" });
    expect(missingLogin(ambient("codex"), wsA)).toBe("");
    expect(missingLogin(ambient("antigravity"), wsA)).toBe("antigravity");
    expect(
      missingLogin({ framework: "antigravity", authMode: "api_key" }, wsA),
    ).toBe("");
    expect(missingLogin(ambient("antigravity"), { name: "old" })).toBe("");
  });
});
