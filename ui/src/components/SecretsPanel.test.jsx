import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import SecretsPanel from "./SecretsPanel.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

describe("SecretsPanel", () => {
  afterEach(() => {
    api.mockReset();
  });

  it("shows a note instead of the list and form when not enabled", async () => {
    api.mockResolvedValueOnce({ enabled: false });
    render(<SecretsPanel showError={() => {}} />);

    expect(await screen.findByText(/not available/i)).toBeInTheDocument();
    expect(screen.queryByLabelText("Secret")).not.toBeInTheDocument();
  });

  it("lists secrets and their keys when enabled", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      secrets: [{ name: "github", keys: ["token", "webhook-secret"] }],
    });
    render(<SecretsPanel showError={() => {}} />);

    expect(await screen.findByText("github")).toBeInTheDocument();
    expect(screen.getByText("token")).toBeInTheDocument();
    expect(screen.getByText("webhook-secret")).toBeInTheDocument();
  });

  // grain/task-110: this panel is the remainder now -- a secret set from
  // the Models tab or from the capability that resolves it is shown
  // there, not here as well.
  it("leaves out the secrets a control elsewhere on the pane owns", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      secrets: [
        { name: "gemini-api-key", keys: ["value"] },
        { name: "gcp-key-minter", keys: ["key.json"] },
        { name: "buildkite", keys: ["token"] },
      ],
    });
    render(
      <SecretsPanel
        showError={() => {}}
        claimed={["gemini-api-key", "gcp-key-minter"]}
      />,
    );

    expect(await screen.findByText("buildkite")).toBeInTheDocument();
    expect(screen.queryByText("gemini-api-key")).not.toBeInTheDocument();
    expect(screen.queryByText("gcp-key-minter")).not.toBeInTheDocument();
  });

  it("leaves repository release secrets on their repository page", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      secrets: [{ name: "repo-release-0123456789abcdef", keys: ["NPM_TOKEN"] }],
    });
    render(<SecretsPanel showError={() => {}} />);

    expect(
      await screen.findByText("No other secrets set."),
    ).toBeInTheDocument();
    expect(screen.queryByText("NPM_TOKEN")).not.toBeInTheDocument();
  });

  it("shows an empty message when every stored secret is owned elsewhere", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      secrets: [{ name: "gemini-api-key", keys: ["value"] }],
    });
    render(<SecretsPanel showError={() => {}} claimed={["gemini-api-key"]} />);

    expect(
      await screen.findByText("No other secrets set."),
    ).toBeInTheDocument();
  });

  it("shows an empty message when enabled with no secrets", async () => {
    api.mockResolvedValueOnce({ enabled: true, secrets: [] });
    render(<SecretsPanel showError={() => {}} />);

    expect(
      await screen.findByText("No other secrets set."),
    ).toBeInTheDocument();
  });

  it("sets a secret via the form and refreshes", async () => {
    api
      .mockResolvedValueOnce({ enabled: true, secrets: [] })
      .mockResolvedValueOnce({})
      .mockResolvedValueOnce({
        enabled: true,
        secrets: [{ name: "github", keys: ["token"] }],
      });
    const user = userEvent.setup();
    render(<SecretsPanel showError={() => {}} />);
    await screen.findByText("No other secrets set.");

    await user.type(screen.getByLabelText("Secret"), "github");
    await user.type(screen.getByLabelText("Key"), "token");
    await user.type(screen.getByLabelText(/Value/), "secret-value");
    await user.click(screen.getByRole("button", { name: "Set secret" }));

    expect(api).toHaveBeenCalledWith("/api/secrets/github/token", {
      method: "PUT",
      body: JSON.stringify({ value: "secret-value" }),
    });
    expect(await screen.findByText("github")).toBeInTheDocument();
  });

  it("deletes a single key", async () => {
    api
      .mockResolvedValueOnce({
        enabled: true,
        secrets: [{ name: "github", keys: ["token"] }],
      })
      .mockResolvedValueOnce({})
      .mockResolvedValueOnce({ enabled: true, secrets: [] });
    const user = userEvent.setup();
    render(<SecretsPanel showError={() => {}} />);
    await screen.findByText("github");

    const confirm = vi.spyOn(window, "confirm").mockReturnValueOnce(false);
    await user.click(screen.getByTitle("delete github/token"));
    expect(api).toHaveBeenCalledTimes(1);
    confirm.mockReturnValueOnce(true);
    await user.click(screen.getByTitle("delete github/token"));
    confirm.mockRestore();

    expect(api).toHaveBeenCalledWith("/api/secrets/github/token", {
      method: "DELETE",
    });
    expect(
      await screen.findByText("No other secrets set."),
    ).toBeInTheDocument();
  });

  it("deletes an entire secret", async () => {
    api
      .mockResolvedValueOnce({
        enabled: true,
        secrets: [{ name: "github", keys: ["token"] }],
      })
      .mockResolvedValueOnce({})
      .mockResolvedValueOnce({ enabled: true, secrets: [] });
    const user = userEvent.setup();
    render(<SecretsPanel showError={() => {}} />);
    await screen.findByText("github");

    const confirm = vi.spyOn(window, "confirm").mockReturnValueOnce(false);
    await user.click(screen.getByRole("button", { name: "Delete secret" }));
    expect(api).toHaveBeenCalledTimes(1);
    confirm.mockReturnValueOnce(true);
    await user.click(screen.getByRole("button", { name: "Delete secret" }));
    confirm.mockRestore();

    expect(api).toHaveBeenCalledWith("/api/secrets/github", {
      method: "DELETE",
    });
    expect(
      await screen.findByText("No other secrets set."),
    ).toBeInTheDocument();
  });

  it("reports the error when setting a secret fails", async () => {
    api
      .mockResolvedValueOnce({ enabled: true, secrets: [] })
      .mockRejectedValueOnce(new Error("value is required"));
    const showError = vi.fn();
    const user = userEvent.setup();
    render(<SecretsPanel showError={showError} />);
    await screen.findByText("No other secrets set.");

    await user.type(screen.getByLabelText("Secret"), "github");
    await user.type(screen.getByLabelText("Key"), "token");
    await user.type(screen.getByLabelText(/Value/), "x");
    await user.click(screen.getByRole("button", { name: "Set secret" }));

    expect(showError).toHaveBeenCalledWith(
      expect.objectContaining({ message: "value is required" }),
    );
  });

  it("refuses a secret or key name that is only spaces", async () => {
    api.mockResolvedValueOnce({ enabled: true, secrets: [] });
    const showError = vi.fn();
    const user = userEvent.setup();
    render(<SecretsPanel showError={showError} />);
    await screen.findByText("No other secrets set.");

    await user.type(screen.getByLabelText("Secret"), "   ");
    await user.type(screen.getByLabelText("Key"), "token");
    await user.type(screen.getByLabelText(/Value/), "x");
    await user.click(screen.getByRole("button", { name: "Set secret" }));

    expect(showError).toHaveBeenCalledWith(
      "Secret and key names cannot be blank",
    );
    expect(api).toHaveBeenCalledTimes(1);
  });
});
