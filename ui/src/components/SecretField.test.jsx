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

import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import SecretField, { SecretFields } from "./SecretField.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

const minter = {
  name: "gcp-key-minter",
  secret: "gcp-key-minter",
  key: "value",
  set: false,
};

describe("SecretField", () => {
  afterEach(() => {
    api.mockReset();
  });

  it("writes the value to the secret and key the API named, then asks for a refresh", async () => {
    api.mockResolvedValueOnce({});
    const onChanged = vi.fn();
    const user = userEvent.setup();
    render(
      <SecretField
        secret={minter}
        showError={() => {}}
        onChanged={onChanged}
      />,
    );

    await user.type(screen.getByLabelText("gcp-key-minter"), "a-key");
    await user.click(screen.getByRole("button", { name: "Set" }));

    expect(api).toHaveBeenCalledWith("/api/secrets/gcp-key-minter/value", {
      method: "PUT",
      body: JSON.stringify({ value: "a-key" }),
    });
    expect(onChanged).toHaveBeenCalled();
  });

  // The value is write-only, so nothing comes back to show: the box is
  // emptied and the chip the reloaded settings bring is what says it
  // landed.
  it("clears the box after a successful set", async () => {
    api.mockResolvedValueOnce({});
    const user = userEvent.setup();
    render(
      <SecretField secret={minter} showError={() => {}} onChanged={() => {}} />,
    );

    const box = screen.getByLabelText("gcp-key-minter");
    await user.type(box, "a-key");
    await user.click(screen.getByRole("button", { name: "Set" }));

    expect(box).toHaveValue("");
  });

  it("cannot be set empty, and cannot be cleared when nothing is set", () => {
    render(
      <SecretField secret={minter} showError={() => {}} onChanged={() => {}} />,
    );

    expect(screen.getByRole("button", { name: "Set" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Clear" })).toBeDisabled();
    expect(screen.getByText("not set")).toBeInTheDocument();
  });

  it("deletes just the one key when cleared", async () => {
    api.mockResolvedValueOnce({});
    const onChanged = vi.fn();
    const user = userEvent.setup();
    render(
      <SecretField
        secret={{
          name: "github-app/app-id",
          secret: "github-app",
          key: "app-id",
          set: true,
        }}
        showError={() => {}}
        onChanged={onChanged}
      />,
    );

    expect(screen.getByText("set")).toBeInTheDocument();
    // A write-only value cannot be read back, so a dismissed confirm
    // sends nothing.
    const confirm = vi.spyOn(window, "confirm").mockReturnValueOnce(false);
    await user.click(screen.getByRole("button", { name: "Clear" }));
    expect(confirm).toHaveBeenCalledWith(
      expect.stringMatching(/^Clear github-app\/app-id\?/),
    );
    expect(api).not.toHaveBeenCalled();

    confirm.mockReturnValueOnce(true);
    await user.click(screen.getByRole("button", { name: "Clear" }));
    confirm.mockRestore();

    expect(api).toHaveBeenCalledWith("/api/secrets/github-app/app-id", {
      method: "DELETE",
    });
    expect(onChanged).toHaveBeenCalled();
  });

  it("reports a failed write instead of pretending it landed", async () => {
    api.mockRejectedValueOnce(new Error("value is required"));
    const showError = vi.fn();
    const onChanged = vi.fn();
    const user = userEvent.setup();
    render(
      <SecretField
        secret={minter}
        showError={showError}
        onChanged={onChanged}
      />,
    );

    await user.type(screen.getByLabelText("gcp-key-minter"), "x");
    await user.click(screen.getByRole("button", { name: "Set" }));

    expect(showError).toHaveBeenCalledWith(
      expect.objectContaining({ message: "value is required" }),
    );
    expect(onChanged).not.toHaveBeenCalled();
  });

  // grain/task-160. The value that goes to the API is the file's own
  // text, newlines intact: a GitHub App's .pem is the credential this is
  // for, and a PEM pasted through a one-line box is the way it usually
  // arrives broken.
  describe("uploading the file a credential came in", () => {
    const privateKey = {
      name: "github-app/private-key",
      secret: "github-app",
      key: "private-key",
      set: false,
    };
    const pem =
      "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEAy8Db\n-----END RSA PRIVATE KEY-----\n";

    it("stores the file's text exactly, line breaks and all", async () => {
      api.mockResolvedValueOnce({});
      const user = userEvent.setup();
      render(
        <SecretField
          secret={privateKey}
          showError={() => {}}
          onChanged={() => {}}
        />,
      );

      await user.upload(
        document.querySelector('input[type="file"]'),
        new File([pem], "grain.private-key.pem"),
      );
      // Loaded, not stored: the write only happens on Set, and the box
      // says which file it is holding until then.
      expect(api).not.toHaveBeenCalled();
      expect(
        await screen.findByText(/grain\.private-key\.pem loaded/),
      ).toBeInTheDocument();

      await user.click(screen.getByRole("button", { name: "Set" }));

      expect(api).toHaveBeenCalledWith("/api/secrets/github-app/private-key", {
        method: "PUT",
        body: JSON.stringify({ value: pem }),
      });
    });

    // Two fields on one capability, two upload buttons: each names the
    // credential it fills, so there is something to press for the right
    // one.
    it("names the credential each upload button belongs to", () => {
      render(
        <SecretField
          secret={privateKey}
          showError={() => {}}
          onChanged={() => {}}
        />,
      );

      expect(
        screen.getByRole("button", {
          name: "Upload a file for github-app/private-key",
        }),
      ).toBeInTheDocument();
    });

    it("reports a file it will not read instead of loading it", async () => {
      const showError = vi.fn();
      const user = userEvent.setup();
      render(
        <SecretField
          secret={privateKey}
          showError={showError}
          onChanged={() => {}}
        />,
      );

      await user.upload(
        document.querySelector('input[type="file"]'),
        new File([""], "nothing.pem"),
      );

      expect(showError).toHaveBeenCalledWith(
        expect.objectContaining({ message: expect.stringMatching(/is empty/) }),
      );
      expect(screen.getByLabelText("github-app/private-key")).toHaveValue("");
      expect(screen.getByRole("button", { name: "Set" })).toBeDisabled();
    });
  });

  describe("SecretFields", () => {
    it("renders nothing for a capability that resolves no credential", () => {
      const { container } = render(
        <SecretFields secrets={[]} showError={() => {}} onChanged={() => {}} />,
      );
      expect(container).toBeEmptyDOMElement();
    });

    it("renders one field per credential", () => {
      render(
        <SecretFields
          secrets={[
            {
              name: "github-app/app-id",
              secret: "github-app",
              key: "app-id",
              set: true,
            },
            {
              name: "github-app/private-key",
              secret: "github-app",
              key: "private-key",
              set: false,
            },
          ]}
          showError={() => {}}
          onChanged={() => {}}
        />,
      );

      expect(screen.getByText("Credentials this needs:")).toBeInTheDocument();
      expect(screen.getByLabelText("github-app/app-id")).toBeInTheDocument();
      expect(
        screen.getByLabelText("github-app/private-key"),
      ).toBeInTheDocument();
    });
  });
});
