import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import StateRepoPanel from "./StateRepoPanel.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

const local = {
  available: true,
  mode: "local",
  branch: "main",
  dir: "/var/lib/grain/data/state-repo",
  head: "abcdef1234567890",
  schemaVersion: 16,
  buildSchemaVersion: 16,
  secretsPublicKey: "grain-secret-pub-v1:AAAA",
  secretsKeyFile: "/var/lib/grain/data/secrets/secrets.key",
};

describe("StateRepoPanel", () => {
  afterEach(() => api.mockReset());

  it("says where state lives, and which key file to back up", async () => {
    api.mockResolvedValueOnce(local);
    render(<StateRepoPanel showError={() => {}} />);

    expect(await screen.findAllByText("grain/config")).not.toHaveLength(0);
    expect(screen.getByText(/Google Drive or GCS/)).toBeInTheDocument();
    expect(
      screen.getByText(/\/var\/lib\/grain\/data\/state-repo/),
    ).toBeInTheDocument();
    // The public key is shown -- it is how an operator checks the key they
    // hold is the one this deployment encrypts to -- and the private half
    // is named as a path, never rendered.
    expect(screen.getByText("grain-secret-pub-v1:AAAA")).toBeInTheDocument();
    // Named more than once on a local-only deployment: once as the file
    // to back up, and once in the warning that says nothing else here is.
    expect(
      screen.getAllByText(/\/var\/lib\/grain\/data\/secrets\/secrets.key/)
        .length,
    ).toBeGreaterThan(0);
  });

  it("switches between internal and external config repository modes", async () => {
    api.mockResolvedValueOnce(local);
    const user = userEvent.setup();
    render(<StateRepoPanel showError={() => {}} />);

    expect(await screen.findAllByText("grain/config")).not.toHaveLength(0);
    expect(
      screen.getByText(/No external config repository is configured/),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", {
        name: "Switch to internal config repository",
      }),
    ).not.toBeInTheDocument();

    await user.click(screen.getByLabelText("Repository URL"));
    await user.paste("  https://github.com/owner/grain-config.git  ");
    await user.clear(screen.getByLabelText("Branch"));
    await user.type(screen.getByLabelText("Branch"), "trunk");
    await user.click(screen.getByLabelText(/Personal access token/));
    await user.paste("ghp_mirror");

    api.mockResolvedValueOnce({
      ...local,
      mode: "external",
      repository: "owner/grain-config",
      remote: "https://github.com/owner/grain-config.git",
      branch: "trunk",
    });
    await user.click(
      screen.getByRole("button", { name: "Use external config repository" }),
    );

    await waitFor(() =>
      expect(api).toHaveBeenLastCalledWith("/api/state-repo/remote", {
        method: "POST",
        body: JSON.stringify({
          remote: "https://github.com/owner/grain-config.git",
          branch: "trunk",
          token: "ghp_mirror",
        }),
      }),
    );

    expect(
      await screen.findAllByText("https://github.com/owner/grain-config.git"),
    ).not.toHaveLength(0);
    expect(
      screen.getAllByText(/External repository \(Infrastructure as Code\)/)
        .length,
    ).toBeGreaterThan(0);
    expect(
      screen.getByRole("button", { name: "Pull now" }),
    ).toBeInTheDocument();
    expect(screen.getByLabelText(/Personal access token/)).toHaveValue("");

    api.mockResolvedValueOnce(local);
    await user.click(
      screen.getByRole("button", {
        name: "Switch to internal config repository",
      }),
    );

    await waitFor(() =>
      expect(api).toHaveBeenLastCalledWith("/api/state-repo/remote", {
        method: "POST",
        body: JSON.stringify({ none: true }),
      }),
    );
    expect(
      screen.queryByRole("button", {
        name: "Switch to internal config repository",
      }),
    ).not.toBeInTheDocument();

    api.mockResolvedValueOnce(local);
    await user.click(screen.getByRole("button", { name: "Export now" }));
    expect(api).toHaveBeenLastCalledWith("/api/state-repo/sync", {
      method: "POST",
      body: JSON.stringify({}),
    });
  });

  it("says so, and offers nothing, when this UI manages no repository", async () => {
    api.mockResolvedValueOnce({ available: false });
    render(<StateRepoPanel showError={() => {}} />);

    expect(
      await screen.findByText(/not running inside a daemon/i),
    ).toBeInTheDocument();
    expect(screen.queryByLabelText("Repository URL")).not.toBeInTheDocument();
  });

  it("says where the encrypted secrets file is backed up, or that it is not", async () => {
    api.mockResolvedValueOnce({
      ...local,
      secretsRepo: "https://github.com/bwsalmon/private-secrets",
      secretsRepoFile: "grain-bwsalmon-grain-home.secrets.enc",
    });
    render(<StateRepoPanel showError={() => {}} />);
    expect(
      await screen.findByText("https://github.com/bwsalmon/private-secrets"),
    ).toBeInTheDocument();
    expect(
      screen.getByText("grain-bwsalmon-grain-home.secrets.enc"),
    ).toBeInTheDocument();

    api.mockReset();
    api.mockResolvedValueOnce(local);
    render(<StateRepoPanel showError={() => {}} />);
    expect(
      await screen.findByText(/on this host alone: nothing pushes it/),
    ).toBeInTheDocument();
  });

  // grain/task-107: the pane reported that repository and could not set
  // it, so an operator with no terminal on the host read a warning they
  // could not act on. Naming one is a setting the daemon's next sync tick
  // acts on, so the pane's job is to post it and paint what came back.
  it("names the repository the encrypted secrets file is backed up to", async () => {
    api.mockResolvedValueOnce(local);
    const user = userEvent.setup();
    render(<StateRepoPanel showError={() => {}} />);
    await screen.findByText(/on this host alone: nothing pushes it/);

    // With the surrounding whitespace a paste out of a browser's address
    // bar carries: what goes to the daemon below is trimmed.
    await user.click(screen.getByLabelText("Secrets repository URL"));
    await user.paste("  https://github.com/owner/grain-secrets.git  ");
    await user.click(screen.getByLabelText(/Credential to push with/));
    await user.paste("ghp_secrets");
    api.mockResolvedValueOnce({
      ...local,
      secretsRepo: "https://github.com/owner/grain-secrets.git",
      secretsRepoFile: "grain-owner-grain-state.secrets.enc",
      secretsRepoBranch: "main",
    });
    await user.click(
      screen.getByRole("button", { name: "Back up secrets here" }),
    );

    await waitFor(() =>
      expect(api).toHaveBeenLastCalledWith("/api/state-repo/secrets-repo", {
        method: "POST",
        body: JSON.stringify({
          remote: "https://github.com/owner/grain-secrets.git",
          branch: "main",
          token: "ghp_secrets",
        }),
      }),
    );
    // The pane now says where the file goes, and the credential is not
    // kept in the form once the daemon has it.
    expect(
      await screen.findByText("grain-owner-grain-state.secrets.enc"),
    ).toBeInTheDocument();
    expect(screen.getByLabelText(/Credential to push with/)).toHaveValue("");
  });

  // Taking it back has to be possible from the same pane -- a repository
  // named by mistake should not need a terminal to undo -- and it is
  // asked for outright, never by clearing a field.
  it("stops backing the file up, and offers that only when it is being backed up", async () => {
    api.mockResolvedValueOnce({
      ...local,
      secretsRepo: "https://github.com/owner/grain-secrets.git",
      secretsRepoFile: "grain-owner-grain-state.secrets.enc",
      secretsRepoBranch: "trunk",
    });
    const user = userEvent.setup();
    render(<StateRepoPanel showError={() => {}} />);
    await screen.findByText("https://github.com/owner/grain-secrets.git");

    // What is in force is what the fields hold, so changing the branch of
    // a repository already named does not mean retyping its URL.
    expect(screen.getByLabelText("Secrets repository URL")).toHaveValue(
      "https://github.com/owner/grain-secrets.git",
    );
    expect(screen.getByLabelText("Secrets repository branch")).toHaveValue(
      "trunk",
    );

    api.mockResolvedValueOnce(local);
    await user.click(
      screen.getByRole("button", { name: "Stop backing them up" }),
    );

    await waitFor(() =>
      expect(api).toHaveBeenLastCalledWith("/api/state-repo/secrets-repo", {
        method: "POST",
        body: JSON.stringify({ none: true }),
      }),
    );
    expect(
      await screen.findByText(/on this host alone: nothing pushes it/),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Stop backing them up" }),
    ).not.toBeInTheDocument();
  });

  it("says when this host's secrets file is sealed to a key it lacks", async () => {
    api.mockResolvedValueOnce({
      ...local,
      secretsError: "secrets: this file is encrypted to a different key",
      secretsFileRecipient: "grain-secret-pub-v1:BBBB",
    });
    render(<StateRepoPanel showError={() => {}} />);

    expect(
      await screen.findByText(/cannot read this host's secrets file/i),
    ).toBeInTheDocument();
    // Which key it wants is the question the operator has to answer, so
    // the pane answers it rather than leaving them to guess.
    expect(screen.getByText("grain-secret-pub-v1:BBBB")).toBeInTheDocument();
  });

  it("imports a private key, and does not keep it", async () => {
    api.mockResolvedValueOnce({
      ...local,
      secretsError: "secrets: this file is encrypted to a different key",
      secretsFileRecipient: "grain-secret-pub-v1:BBBB",
    });
    const user = userEvent.setup();
    render(<StateRepoPanel showError={() => {}} />);
    await screen.findByText(/cannot read this host's secrets file/i);

    await user.click(screen.getByLabelText(/Import a private key/));
    await user.paste("grain-secret-key-v1:BBBB");
    api.mockResolvedValueOnce({
      ...local,
      secretsPublicKey: "grain-secret-pub-v1:BBBB",
    });
    await user.click(screen.getByRole("button", { name: "Import key" }));

    await waitFor(() =>
      expect(api).toHaveBeenLastCalledWith("/api/state-repo/secrets-key", {
        method: "POST",
        body: JSON.stringify({ key: "grain-secret-key-v1:BBBB" }),
      }),
    );
    expect(screen.getByLabelText(/Import a private key/)).toHaveValue("");
    expect(
      screen.queryByText(/cannot read this host's secrets file/i),
    ).not.toBeInTheDocument();
  });

  // What a merged settings change took away, on a deployment that had
  // nowhere to say so (docs/state-pane-reset-notice.md). This pane and
  // the deployment's journal are the only places those values are still
  // written down, so the notice carries them rather than pointing at a
  // log an operator would have to scroll.
  describe("a settings reset nobody could be told about", () => {
    const reset = {
      ...local,
      resetNotices: [
        {
          at: "2026-09-07T10:15:00Z",
          commit: "0123456789abcdef0123456789abcdef01234567",
          reason:
            "this deployment's state repository is local only, so there is nowhere to say it",
          values: [
            {
              path: "tables/grain_config.json",
              column: "time_zone",
              row: "id = 1",
              from: "'Europe/London'",
              to: "'America/Los_Angeles'",
            },
          ],
          moreValues: 2,
        },
      ],
    };

    it("says what went, which merge did it, and why nobody was told", async () => {
      api.mockResolvedValueOnce(reset);
      render(<StateRepoPanel showError={() => {}} />);

      // The values themselves, which exist nowhere else a reader will
      // find them.
      expect(
        await screen.findByText(/time_zone was 'Europe\/London'/),
      ).toBeVisible();
      // The merge that did it, so somebody can go and look at the change.
      expect(screen.getByText("01234567")).toBeInTheDocument();
      // Its own reason for being on screen: the rule -- shown only when
      // the comment could not be posted -- is not one a reader should
      // have to be taught.
      expect(screen.getByText(/so there is nowhere to say it/)).toBeVisible();
      // The count is of everything lost, not of what fits on the pane,
      // and the rest are named as being in the journal.
      expect(screen.getByText(/replaced 3 settings values/)).toBeVisible();
      expect(
        screen.getByText(/and 2 more, in this deployment's journal/),
      ).toBeVisible();
    });

    it("dismisses it for the deployment, and tells the chrome outside", async () => {
      api
        .mockResolvedValueOnce(reset)
        .mockResolvedValueOnce({ ...local, resetNotices: [] });
      const onStatusChange = vi.fn();
      const user = userEvent.setup();
      render(
        <StateRepoPanel showError={() => {}} onStatusChange={onStatusChange} />,
      );

      await screen.findByText(/time_zone was 'Europe\/London'/);
      await user.click(screen.getByRole("button", { name: "Dismiss" }));

      expect(api).toHaveBeenCalledWith("/api/state-repo/reset-notice", {
        method: "POST",
        body: JSON.stringify({}),
      });
      // Gone, and gone for good: there is nothing here to un-dismiss,
      // because the event is over and the journal keeps the values.
      await waitFor(() =>
        expect(
          screen.queryByText(/time_zone was 'Europe\/London'/),
        ).not.toBeInTheDocument(),
      );
      // And the rail's dot goes with it rather than waiting out a poll.
      expect(onStatusChange).toHaveBeenCalled();
    });

    it("says nothing when every reset was reported where it happened", async () => {
      api.mockResolvedValueOnce(local);
      render(<StateRepoPanel showError={() => {}} />);

      await screen.findAllByText("grain/config");
      expect(
        screen.queryByRole("button", { name: "Dismiss" }),
      ).not.toBeInTheDocument();
    });
  });

  it("warns when the repository was written by a different schema", async () => {
    api.mockResolvedValueOnce({
      ...local,
      schemaVersion: 15,
      buildSchemaVersion: 16,
    });
    render(<StateRepoPanel showError={() => {}} />);

    expect(
      await screen.findByText(/does not migrate a dump/i),
    ).toBeInTheDocument();
  });

  // Reading the repository, from the pane that already tells its story.
  // There is nowhere else: it is controller-local, so no forge page
  // shows it and no remote can be cloned from a browser.
  it("browses the exported files, saying which commit they are of", async () => {
    api.mockImplementation((path) => {
      if (path === "/api/state-repo") {
        return Promise.resolve({ ...local, syncedAt: "2026-09-19T05:26:00Z" });
      }
      if (path === "/api/state-repo/files") {
        return Promise.resolve({
          head: "abcdef1234567890",
          entries: [
            { path: "memory", dir: true },
            { path: "memory/deployment.md", size: 3872 },
            { path: "tables", dir: true },
            { path: "tables/grain_config.json", size: 2048 },
          ],
        });
      }
      return Promise.resolve({
        path: "memory/deployment.md",
        size: 3872,
        content: "# what grain has learned\n",
      });
    });
    const user = userEvent.setup();
    render(<StateRepoPanel showError={() => {}} />);

    await user.click(await screen.findByRole("button", { name: "Show files" }));
    expect(await screen.findByText("deployment.md")).toBeInTheDocument();
    expect(screen.getByText("grain_config.json")).toBeInTheDocument();
    expect(screen.getByText(/showing abcdef1/)).toBeInTheDocument();

    await user.click(screen.getByText("deployment.md"));
    expect(
      await screen.findByRole("heading", { name: "what grain has learned" }),
    ).toBeInTheDocument();
  });
});

// A failed status read used to render nothing at all once the snackbar
// faded.
describe("StateRepoPanel when its status read fails", () => {
  afterEach(() => {
    api.mockReset();
  });

  it("says so, and Try again reads it again", async () => {
    api.mockRejectedValueOnce(new Error("store locked"));
    api.mockResolvedValueOnce({ available: false });
    render(<StateRepoPanel showError={() => {}} />);

    expect(
      await screen.findByText(/could not be read \(store locked\)/),
    ).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(
      await screen.findByText(/not running inside a daemon/),
    ).toBeInTheDocument();
    expect(api).toHaveBeenCalledTimes(2);
  });
});
