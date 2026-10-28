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
  it("guides a VM backend to guest-base and both contracts, and saves the image", () => {
    const { s, saveChoice } = section({
      sandboxBackend: "granule",
      sandboxImageBackend: "granule",
      sandboxImage: "",
      sandboxImageDefault: "ghcr.io/bwsalmon/grain/guest:sha-abc",
      sandboxBaseImage: "ghcr.io/bwsalmon/grain/guest-base:sha-abc",
      sandboxContractVersion: 1,
      sandboxMinimumContractVersion: 1,
      sandboxGranuleContractVersion: 1,
    });
    render(<CustomSandboxImageCard s={s} />);

    const box = screen.getByLabelText("Custom sandbox image (OCI reference)");
    expect(box).toHaveAttribute(
      "placeholder",
      "ghcr.io/bwsalmon/grain/guest:sha-abc",
    );
    expect(screen.getByTestId("sandbox-image-dockerfile")).toHaveTextContent(
      "FROM ghcr.io/bwsalmon/grain/guest-base:sha-abc",
    );
    const versions = screen.getByTestId("sandbox-contract-versions");
    expect(versions).toHaveTextContent("dev.grain.sandbox.contract = 1");
    expect(versions).toHaveTextContent("dev.granule.guest.contract = 1");
    expect(screen.queryByTestId("sandbox-contract-checklist")).toBeNull();
    expect(screen.queryByText(/must carry/i)).toBeNull();
    const inherits = screen.getByTestId("sandbox-image-inherits");
    expect(inherits).toHaveTextContent(
      "guest-base already satisfies the entire sandbox contract",
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

  it("guides a pod backend to k8s-sandbox-base and shows the contract warning", () => {
    const { s } = section({
      sandboxBackend: "kubernetes",
      sandboxImage: "registry.example/team/sandbox:v1",
      sandboxBaseImage: "ghcr.io/bwsalmon/grain/k8s-sandbox-base:sha-abc",
      sandboxImageWarning:
        "Custom sandbox image registry.example/team/sandbox:v1 needs rebuilding FROM ghcr.io/bwsalmon/grain/k8s-sandbox-base:sha-abc",
      sandboxContractVersion: 1,
      sandboxMinimumContractVersion: 1,
    });
    render(<CustomSandboxImageCard s={s} />);

    expect(screen.getByTestId("sandbox-image-warning")).toHaveTextContent(
      "needs rebuilding FROM",
    );
    expect(screen.getByTestId("sandbox-image-dockerfile")).toHaveTextContent(
      "FROM ghcr.io/bwsalmon/grain/k8s-sandbox-base:sha-abc",
    );
    expect(
      screen.getByTestId("sandbox-contract-versions"),
    ).not.toHaveTextContent("dev.granule.guest.contract");
    expect(screen.queryByTestId("sandbox-contract-checklist")).toBeNull();
    const inherits = screen.getByTestId("sandbox-image-inherits");
    expect(inherits).toHaveTextContent(
      "k8s-sandbox-base already satisfies the entire sandbox contract",
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
});
