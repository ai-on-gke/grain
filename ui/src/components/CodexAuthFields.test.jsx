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

import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import CodexAuthFields from "./CodexAuthFields.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));
const backend = {
  id: "account-one",
  framework: "codex",
  authMode: "subscription",
};
const url = "/api/agent-backends/account-one/codex-auth";

describe("CodexAuthFields", () => {
  afterEach(() => api.mockReset());

  it("uploads, replaces and clears a backend login without changing the shared login", async () => {
    api.mockResolvedValueOnce({ enabled: true, set: false, sharedSet: true });
    const user = userEvent.setup();
    const { container } = render(
      <CodexAuthFields
        backend={backend}
        onChange={vi.fn()}
        showError={vi.fn()}
      />,
    );
    await screen.findByText(/Using the existing shared subscription login/);
    expect(
      screen.getByRole("button", { name: "Clear backend login" }),
    ).toBeDisabled();
    for (const token of ["first", "replacement"]) {
      const value = JSON.stringify({ tokens: { refresh_token: token } });
      await user.upload(
        container.querySelector('input[type="file"]'),
        new File([value], "auth.json", { type: "application/json" }),
      );
      await screen.findByText(/not stored yet/);
      expect(screen.getByLabelText("Codex auth.json")).toHaveValue(value);
      api.mockResolvedValueOnce({ enabled: true, set: true, sharedSet: true });
      await user.click(screen.getByRole("button", { name: "Save login" }));
      await screen.findByText(
        "Subscription login configured for this backend.",
      );
      expect(api).toHaveBeenLastCalledWith(url, {
        method: "PUT",
        body: JSON.stringify({ value }),
      });
      expect(screen.getByLabelText("Codex auth.json")).toHaveValue("");
    }
    api.mockResolvedValueOnce({ enabled: true, set: false, sharedSet: true });
    await user.click(
      screen.getByRole("button", { name: "Clear backend login" }),
    );
    await screen.findByText(/Using the existing shared subscription login/);
    expect(api).toHaveBeenLastCalledWith(url, { method: "DELETE" });
  });

  it("says when grain could no longer refresh the login in use", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      set: true,
      sharedSet: true,
      refreshError: "the subscription login can no longer be refreshed",
      sharedRefreshError: "the shared login is refused too",
    });
    render(
      <CodexAuthFields
        backend={backend}
        onChange={vi.fn()}
        showError={vi.fn()}
      />,
    );
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "the subscription login can no longer be refreshed",
    );
    expect(
      screen.queryByText("the shared login is refused too"),
    ).not.toBeInTheDocument();
  });

  it("preserves the configured status and pending file when replacement fails", async () => {
    api.mockResolvedValueOnce({ enabled: true, set: true, sharedSet: false });
    const showError = vi.fn();
    const user = userEvent.setup();
    const { container } = render(
      <CodexAuthFields
        backend={backend}
        onChange={vi.fn()}
        showError={showError}
      />,
    );
    await screen.findByText("Subscription login configured for this backend.");
    await user.upload(
      container.querySelector('input[type="file"]'),
      new File(["[]"], "auth.json"),
    );
    await screen.findByText(/not stored yet/);
    const error = new Error("auth.json is not a JSON object");
    api.mockRejectedValueOnce(error);
    await user.click(screen.getByRole("button", { name: "Save login" }));
    await waitFor(() => expect(showError).toHaveBeenCalledWith(error));
    expect(screen.getByLabelText("Codex auth.json")).toHaveValue("[]");
    expect(
      screen.getByText("Subscription login configured for this backend."),
    ).toBeInTheDocument();
  });

  it("notifies the model picker after authentication changes", async () => {
    api
      .mockResolvedValueOnce({ enabled: true, set: false, sharedSet: false })
      .mockResolvedValueOnce({ enabled: true, set: true, sharedSet: false });
    const onCredentialChange = vi.fn();
    const user = userEvent.setup();
    render(
      <CodexAuthFields
        backend={backend}
        onCredentialChange={onCredentialChange}
        showError={vi.fn()}
      />,
    );
    fireEvent.change(screen.getByLabelText("Codex auth.json"), {
      target: { value: '{"tokens":{}}' },
    });
    await user.click(screen.getByRole("button", { name: "Save login" }));
    expect(onCredentialChange).toHaveBeenCalledOnce();
  });
});
