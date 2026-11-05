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

import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import CustomSandboxImageCard from "./CustomSandboxImageCard.jsx";

function section(settings) {
  const saveChoice = vi.fn();
  return {
    saveChoice,
    s: {
      settings,
      saveChoice,
      onTextBlur: (key) => (evt) =>
        saveChoice({ [key]: evt.target.value.trim() }),
      onEnterKey: () => {},
    },
  };
}

describe("CustomSandboxImageCard", () => {
  it("guides a VM backend to sandbox-base and both contracts, and saves the image", () => {
    const { s, saveChoice } = section({
      sandboxBackend: "granule",
      sandboxImageBackend: "granule",
      sandboxImage: "",
      sandboxImageDefault: "docker.io/grainai/sandbox:sha-abc",
      sandboxBaseImage: "docker.io/grainai/sandbox-base:sha-abc",
      sandboxContractVersion: 1,
      sandboxMinimumContractVersion: 1,
      sandboxGranuleContractVersion: 1,
    });
    render(<CustomSandboxImageCard s={s} />);

    const box = screen.getByLabelText("Custom sandbox image (OCI reference)");
    expect(box).toHaveAttribute(
      "placeholder",
      "docker.io/grainai/sandbox:sha-abc",
    );
    expect(screen.getByTestId("sandbox-image-dockerfile")).toHaveTextContent(
      "FROM docker.io/grainai/sandbox-base:sha-abc",
    );
    const versions = screen.getByTestId("sandbox-contract-versions");
    expect(versions).toHaveTextContent("dev.grain.sandbox.contract = 1");
    expect(versions).toHaveTextContent("dev.granule.guest.contract = 1");
    expect(screen.queryByTestId("sandbox-contract-checklist")).toBeNull();
    expect(screen.queryByText(/must carry/i)).toBeNull();
    const inherits = screen.getByTestId("sandbox-image-inherits");
    expect(inherits).toHaveTextContent(
      "sandbox-base already satisfies the entire sandbox contract",
    );
    expect(inherits).toHaveTextContent("grain-connect");
    expect(screen.getByTestId("sandbox-image-todo")).toHaveTextContent(
      "start FROM it and install the packages",
    );
    expect(screen.getByTestId("sandbox-contract-reference")).toHaveTextContent(
      "grain sandbox-image check --probe",
    );
    expect(
      screen.getByText(/does not need rebuilding on every grain upgrade/i),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "docs/sandbox-image-contract.md" }),
    ).toHaveAttribute(
      "href",
      expect.stringContaining("sandbox-image-contract.md"),
    );

    fireEvent.change(box, {
      target: { value: " registry.example/team/guest:v1 " },
    });
    fireEvent.blur(box);
    expect(saveChoice).toHaveBeenCalledWith({
      sandboxImage: "registry.example/team/guest:v1",
    });
  });

  it("guides a pod backend to sandbox-k8s-base and shows the contract warning", () => {
    const { s } = section({
      sandboxBackend: "kubernetes",
      sandboxImage: "registry.example/team/sandbox:v1",
      sandboxBaseImage: "docker.io/grainai/sandbox-k8s-base:sha-abc",
      sandboxImageWarning:
        "Custom sandbox image registry.example/team/sandbox:v1 needs rebuilding FROM docker.io/grainai/sandbox-k8s-base:sha-abc",
      sandboxContractVersion: 1,
      sandboxMinimumContractVersion: 1,
    });
    render(<CustomSandboxImageCard s={s} />);

    expect(screen.getByTestId("sandbox-image-warning")).toHaveTextContent(
      "needs rebuilding FROM",
    );
    expect(screen.getByTestId("sandbox-image-dockerfile")).toHaveTextContent(
      "FROM docker.io/grainai/sandbox-k8s-base:sha-abc",
    );
    expect(
      screen.getByTestId("sandbox-contract-versions"),
    ).not.toHaveTextContent("dev.granule.guest.contract");
    expect(screen.queryByTestId("sandbox-contract-checklist")).toBeNull();
    const inherits = screen.getByTestId("sandbox-image-inherits");
    expect(inherits).toHaveTextContent(
      "sandbox-k8s-base already satisfies the entire sandbox contract",
    );
    expect(inherits).not.toHaveTextContent("grain-connect");
  });

  it("confirms a verified custom image at the current contract", () => {
    const { s } = section({
      sandboxBackend: "container",
      sandboxImage: "registry.example/team/sandbox:v1",
      sandboxImageCheck: {
        verified: true,
        contract: { summary: "grain sandbox contract: v1 (current)" },
      },
    });
    render(<CustomSandboxImageCard s={s} />);
    expect(screen.getByTestId("sandbox-image-contract-ok")).toHaveTextContent(
      "v1 (current)",
    );
  });

  it("reverts to the default image when cleared or reset", () => {
    const { s, saveChoice } = section({
      sandboxBackend: "kubernetes-granule",
      sandboxImage: "registry.example/team/guest:v1",
      sandboxImageDefault: "docker.io/grainai/sandbox:sha-abc",
      sandboxImageWarning: "Custom sandbox image warning",
    });
    const { rerender } = render(<CustomSandboxImageCard s={s} />);

    const resetBtn = screen.getByTestId("sandboxImage-reset");
    expect(resetBtn).toHaveTextContent("Reset to default");
    fireEvent.click(resetBtn);
    expect(saveChoice).toHaveBeenCalledWith({ sandboxImage: "" });

    // Clearing the text input and blurring also saves "".
    saveChoice.mockClear();
    const box = screen.getByLabelText("Custom sandbox image (OCI reference)");
    fireEvent.change(box, { target: { value: "   " } });
    fireEvent.blur(box);
    expect(saveChoice).toHaveBeenCalledWith({ sandboxImage: "" });

    // Once sandboxImage is empty, the reset button and stale warnings are hidden.
    const cleared = section({
      sandboxBackend: "kubernetes-granule",
      sandboxImage: "",
      sandboxImageDefault: "docker.io/grainai/sandbox:sha-abc",
      sandboxImageWarning: "",
      sandboxImageCheck: null,
    });
    rerender(<CustomSandboxImageCard s={cleared.s} />);
    expect(screen.queryByTestId("sandboxImage-reset")).toBeNull();
    expect(screen.queryByTestId("sandbox-image-warning")).toBeNull();
    expect(screen.queryByTestId("sandbox-image-contract-ok")).toBeNull();
  });

  it("offers to clear a k8s_granule_image holding the custom image's reference on kubernetes-granule", () => {
    const ref = "us-docker.pkg.dev/example-project/grain/guest:sha-31d0973";
    const settings = {
      sandboxBackend: "kubernetes-granule",
      sandboxImageBackend: "kubernetes-granule",
      sandboxImage: ref,
      k8sGranuleImage: ref,
      k8sGranuleImageMirror: "us-docker.pkg.dev/example-project/grain/guest",
    };
    const { s, saveChoice } = section(settings);
    const { rerender } = render(<CustomSandboxImageCard s={s} />);
    const pin = screen.getByTestId("sandbox-image-k8s-granule-pin");
    expect(pin).toHaveTextContent("k8s_granule_image");
    expect(pin).toHaveTextContent(ref);
    expect(pin).toHaveTextContent("read as mirror repository");

    // Resetting the custom image alone leaves the pin, and the card still
    // says so, now offering to clear just the pin.
    fireEvent.click(screen.getByTestId("sandboxImage-reset"));
    expect(saveChoice).toHaveBeenLastCalledWith({ sandboxImage: "" });
    s.settings = { ...settings, sandboxImage: "" };
    rerender(<CustomSandboxImageCard s={{ ...s }} />);
    expect(
      screen.getByTestId("sandbox-image-k8s-granule-pin"),
    ).toHaveTextContent("this card just cleared");
    fireEvent.click(screen.getByTestId("sandbox-image-k8s-granule-pin-clear"));
    expect(saveChoice).toHaveBeenLastCalledWith({ k8sGranuleImage: "" });
  });

  it("says nothing of k8s_granule_image on other backends or when it differs", () => {
    const ref = "us-docker.pkg.dev/example-project/grain/guest:sha-31d0973";
    const { s } = section({
      sandboxBackend: "kubernetes-granule",
      sandboxImageBackend: "kubernetes-granule",
      sandboxImage: "registry.example/team/guest:v1",
      k8sGranuleImage: ref,
    });
    render(<CustomSandboxImageCard s={s} />);
    expect(screen.queryByTestId("sandbox-image-k8s-granule-pin")).toBeNull();
  });
});
