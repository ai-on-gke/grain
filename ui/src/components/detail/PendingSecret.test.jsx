import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import DetailOverlay from "../DetailOverlay.jsx";
import api from "../../api.js";
import { resetDetailMocks, baseTask, config } from "./testFixtures.js";

vi.mock("../../api.js", () => ({ default: vi.fn().mockResolvedValue(null) }));
vi.mock("../../attachments.js", () => ({ default: vi.fn() }));

describe("DetailOverlay pending secret", () => {
  afterEach(resetDetailMocks);

  // grain/task-230: a run that called request_secret parks its task the
  // way a question does, and the pane offers a box for the one
  // credential it asked for. The value must not go anywhere near the
  // conversation -- these three pin that it is sent to the task's own
  // secret endpoint, and that a task nobody asked a secret of is offered
  // nothing.
  it("offers a write-only box for the secret a parked run asked for", async () => {
    const act = vi.fn();
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "awaiting_reply",
          pendingSecret: {
            name: "stripe-api-key",
            secret: "stripe-api-key",
            key: "value",
            set: false,
          },
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
        showError={vi.fn()}
      />,
    );

    const box = screen.getByLabelText("Value for stripe-api-key");
    expect(box).toHaveAttribute("type", "password");
    // Pasted, the way a credential reaches a write-only box.
    await user.click(box);
    await user.paste("sk_live_1");
    await user.click(screen.getByRole("button", { name: "Set secret" }));

    expect(api).toHaveBeenCalledWith("/api/tasks/12/secret", {
      method: "PUT",
      body: JSON.stringify({ value: "sk_live_1" }),
    });
    // Never as a comment: that is the whole distinction this box exists
    // to draw.
    expect(api).not.toHaveBeenCalledWith(
      "/api/tasks/12/comments",
      expect.anything(),
    );
  });

  it("keeps a rejected value in the box rather than clearing it", async () => {
    const showError = vi.fn();
    const user = userEvent.setup();
    api.mockRejectedValueOnce(new Error("no secrets store"));
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "awaiting_reply",
          pendingSecret: {
            name: "stripe-api-key",
            secret: "stripe-api-key",
            key: "value",
            set: false,
          },
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={showError}
      />,
    );

    await user.click(screen.getByLabelText("Value for stripe-api-key"));
    await user.paste("sk_live_1");
    await user.click(screen.getByRole("button", { name: "Set secret" }));

    expect(showError).toHaveBeenCalled();
    expect(screen.getByLabelText("Value for stripe-api-key")).toHaveValue(
      "sk_live_1",
    );
  });

  // grain/task-160: the credential a run asks for can be a file -- a
  // GitHub App's .pem is the case this was built for -- and its line
  // breaks have to reach the store as they were on disk. The alert's own
  // hidden input is the one addressed here: the reply box below has an
  // attachment picker with a file input of its own.
  it("loads the secret a parked run asked for from a file, newlines intact", async () => {
    const user = userEvent.setup();
    const pem =
      "-----BEGIN RSA PRIVATE KEY-----\nMIIEow\n-----END RSA PRIVATE KEY-----\n";
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "awaiting_reply",
          pendingSecret: {
            name: "github-app/private-key",
            secret: "github-app",
            key: "private-key",
            set: false,
          },
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={vi.fn()}
      />,
    );

    const alert = screen.getByRole("alert");
    await user.upload(
      alert.querySelector('input[type="file"]'),
      new File([pem], "grain.private-key.pem"),
    );
    expect(
      await screen.findByText(/grain\.private-key\.pem loaded/),
    ).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Set secret" }));

    expect(api).toHaveBeenCalledWith("/api/tasks/12/secret", {
      method: "PUT",
      body: JSON.stringify({ value: pem }),
    });
  });

  it("offers no secret box on a task with no pending secret", () => {
    render(
      <DetailOverlay
        task={baseTask}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={vi.fn()}
      />,
    );

    expect(
      screen.queryByRole("button", { name: "Set secret" }),
    ).not.toBeInTheDocument();
  });
});
