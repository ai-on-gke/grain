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
import SandboxesSection from "./SandboxesSection.jsx";

describe("SandboxesSection", () => {
  it("renders Kubernetes and Kubernetes Granule backend options and saves Kubernetes settings", () => {
    const saveChoice = vi.fn();
    const onTextBlur = (key) => (evt) =>
      saveChoice({ [key]: evt.target.value.trim() });
    const s = {
      settings: {
        sandboxBackend: "kubernetes-granule",
        sandboxBackends: [
          "container",
          "granule",
          "kubernetes",
          "kubernetes-granule",
        ],
        k8sNamespace: "grain",
        k8sNamespaceDefault: "grain",
        k8sSandboxImageDefault: "ghcr.io/bwsalmon/grain/k8s-sandbox:latest",
        k8sGranuleImageDefault: "ghcr.io/bwsalmon/grain/guest:latest",
        k8sWorkspaceDefault: "/home/debian",
        k8sNodeSelector: ["pool=kvm"],
        k8sPrivileged: false,
        k8sNetworkPolicy: false,
        k8sDeploymentDefault: "grain",
        k8sContainerDefault: "grain",
        restartRequired: [
          "sandboxBackend",
          "k8sNamespace",
          "k8sNodeSelector",
          "k8sPrivileged",
          "k8sNetworkPolicy",
        ],
      },
      saveChoice,
      onTextBlur,
      onIntBlur: () => () => {},
      onFloatBlur: () => () => {},
      onEnterKey: () => {},
      markDirty: () => {},
    };

    render(<SandboxesSection s={s} />);

    expect(
      screen.getByRole("option", {
        name: "Kubernetes — container pod per run (no VM)",
      }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("option", {
        name: "Kubernetes — Granule VM per run",
      }),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        /Each run boots a Granule microVM with its own guest kernel over \/dev\/kvm inside a privileged Kubernetes pod\./i,
      ),
    ).toBeInTheDocument();

    const selectorInput = screen.getByLabelText("Kubernetes node selector");
    fireEvent.change(selectorInput, {
      target: { value: "pool=kvm, zone=us-central1-a" },
    });
    fireEvent.blur(selectorInput);
    expect(saveChoice).toHaveBeenCalledWith({
      k8sNodeSelector: ["pool=kvm", "zone=us-central1-a"],
    });

    const privCheckbox = screen.getByRole("checkbox", {
      name: /Run Kubernetes sandbox containers privileged/i,
    });
    fireEvent.click(privCheckbox);
    expect(saveChoice).toHaveBeenCalledWith({ k8sPrivileged: true });
  });

  it("saves a cleared priority class as empty rather than as the default", () => {
    const saveChoice = vi.fn();
    const s = {
      settings: {
        sandboxBackend: "kubernetes-granule",
        sandboxBackends: ["kubernetes", "kubernetes-granule"],
        k8sPriorityClassInteractive: "grain-interactive",
        k8sPriorityClassAgent: "grain-agent",
        k8sPriorityClassCi: "grain-ci",
        k8sPriorityClassCiDefault: "grain-ci",
      },
      saveChoice,
      onTextBlur: (key) => (evt) =>
        saveChoice({ [key]: evt.target.value.trim() }),
      onIntBlur: () => () => {},
      onFloatBlur: () => () => {},
      onEnterKey: () => {},
      markDirty: () => {},
    };

    render(<SandboxesSection s={s} />);

    const ci = screen.getByLabelText("Priority class for CI jobs");
    expect(ci).toHaveValue("grain-ci");
    fireEvent.change(ci, { target: { value: "" } });
    fireEvent.blur(ci);
    expect(saveChoice).toHaveBeenCalledWith({ k8sPriorityClassCi: "" });
    expect(
      screen.getByLabelText("Priority class for interactive sessions"),
    ).toHaveValue("grain-interactive");
  });

  it("allows selecting the first backend option when sandboxBackend is initially empty", () => {
    const saveChoice = vi.fn();
    const s = {
      settings: {
        sandboxBackend: "",
        sandboxBackends: ["kubernetes", "kubernetes-granule"],
      },
      saveChoice,
      onTextBlur: () => () => {},
      onIntBlur: () => () => {},
      onFloatBlur: () => () => {},
      onEnterKey: () => {},
      markDirty: () => {},
    };

    render(<SandboxesSection s={s} />);

    const backendSelect = screen.getByLabelText("Sandbox backend");
    fireEvent.change(backendSelect, { target: { value: "kubernetes" } });
    expect(saveChoice).toHaveBeenCalledWith({ sandboxBackend: "kubernetes" });
  });

  it("prefills Kubernetes defaults as values with default chip, switches to custom on edit, resets to empty, and does not save unchanged defaults", () => {
    const saveChoice = vi.fn();
    const unchanged = vi.fn();
    const s = {
      settings: {
        sandboxBackend: "kubernetes",
        sandboxBackends: ["kubernetes", "kubernetes-granule"],
        daemonBuildVersion: "v2.3.0-dev.114+8e1a209",
        k8sNamespace: "",
        k8sNamespaceDefault: "grain",
        k8sSandboxImage: "",
        k8sSandboxImageDefault:
          "ghcr.io/bwsalmon/grain/k8s-sandbox:sha-8e1a209",
        k8sGranuleImage: "",
        k8sGranuleImageDefault: "ghcr.io/bwsalmon/grain/guest:sha-8e1a209",
        k8sWorkspace: "",
        k8sWorkspaceDefault: "/home/agent",
        k8sDeployment: "",
        k8sDeploymentDefault: "grain",
        k8sContainer: "",
        k8sContainerDefault: "grain",
        k8sKubectl: "",
        k8sKubectlDefault: "kubectl",
        sources: {
          k8sNamespace: "default",
          k8sSandboxImage: "default",
          k8sGranuleImage: "default",
          k8sWorkspace: "default",
          k8sDeployment: "default",
          k8sContainer: "default",
          k8sKubectl: "default",
        },
      },
      saveChoice,
      unchanged,
      onTextBlur: () => () => {},
      onIntBlur: () => () => {},
      onFloatBlur: () => () => {},
      onEnterKey: () => {},
      markDirty: () => {},
    };

    render(<SandboxesSection s={s} />);

    // 1. Effective defaults are pre-filled as input values, with "default" chips
    const nsInput = screen.getByLabelText("Kubernetes namespace");
    const sandboxImgInput = screen.getByLabelText(
      "Kubernetes sandbox pod image",
    );
    const granuleImgInput = screen.getByLabelText(
      "Kubernetes Granule VM pod image",
    );
    const workspaceInput = screen.getByLabelText(
      "Kubernetes workspace directory",
    );
    const deploymentInput = screen.getByLabelText("Kubernetes Deployment");
    const containerInput = screen.getByLabelText("Kubernetes container");
    const kubectlInput = screen.getByLabelText("Kubectl binary");

    expect(nsInput).toHaveValue("grain");
    expect(sandboxImgInput).toHaveValue(
      "ghcr.io/bwsalmon/grain/k8s-sandbox:sha-8e1a209",
    );
    expect(granuleImgInput).toHaveValue(
      "ghcr.io/bwsalmon/grain/guest:sha-8e1a209",
    );
    expect(workspaceInput).toHaveValue("/home/agent");
    expect(deploymentInput).toHaveValue("grain");
    expect(containerInput).toHaveValue("grain");
    expect(kubectlInput).toHaveValue("kubectl");

    expect(screen.getByTestId("k8sNamespace-source-chip")).toHaveTextContent(
      "default",
    );
    expect(screen.getByTestId("k8sSandboxImage-source-chip")).toHaveTextContent(
      "default",
    );
    expect(screen.queryByTestId("k8sNamespace-reset")).not.toBeInTheDocument();

    // 2. Help text includes the daemon's build version
    expect(
      screen.getAllByText(
        /matches this daemon's build v2\.3\.0-dev\.114\+8e1a209/,
      ),
    ).toHaveLength(2);

    // 3. Blurring an unchanged default stores nothing (does not call saveChoice)
    fireEvent.focus(nsInput);
    fireEvent.blur(nsInput);
    expect(saveChoice).not.toHaveBeenCalled();

    // 4. Editing turns the chip into "custom" and reveals "Reset to default"
    fireEvent.change(nsInput, { target: { value: "custom-ns" } });
    expect(screen.getByTestId("k8sNamespace-source-chip")).toHaveTextContent(
      "custom",
    );
    const resetBtn = screen.getByTestId("k8sNamespace-reset");
    expect(resetBtn).toBeInTheDocument();
    expect(resetBtn).toHaveTextContent("Reset to default");

    fireEvent.blur(nsInput);
    expect(saveChoice).toHaveBeenCalledWith({ k8sNamespace: "custom-ns" });

    // 5. Clicking "Reset to default" restores the default value, flips chip back to "default", and saves empty ("")
    saveChoice.mockClear();
    fireEvent.click(resetBtn);
    expect(nsInput).toHaveValue("grain");
    expect(screen.getByTestId("k8sNamespace-source-chip")).toHaveTextContent(
      "default",
    );
    expect(screen.queryByTestId("k8sNamespace-reset")).not.toBeInTheDocument();
    expect(saveChoice).toHaveBeenCalledWith({ k8sNamespace: "" });
  });

  it("shows a warning alert in the Kubernetes card when an image does not resolve", () => {
    const s = {
      settings: {
        sandboxBackend: "kubernetes",
        sandboxBackends: ["kubernetes"],
        k8sSandboxImageWarning:
          'Sandbox pod image "ghcr.io/bwsalmon/grain/k8s-sandbox:missing" did not resolve: image manifest not found',
      },
      saveChoice: vi.fn(),
      onTextBlur: () => () => {},
      onIntBlur: () => () => {},
      onFloatBlur: () => () => {},
      onEnterKey: () => {},
      markDirty: () => {},
    };

    render(<SandboxesSection s={s} />);

    const alert = screen.getByTestId("k8s-image-warning");
    expect(alert).toBeInTheDocument();
    expect(alert).toHaveTextContent(
      'Sandbox pod image "ghcr.io/bwsalmon/grain/k8s-sandbox:missing" did not resolve',
    );
  });

  it("renders unavailable backends disabled with their reasons and shows selected backend isolation help", () => {
    const s = {
      settings: {
        sandboxBackend: "kubernetes",
        sandboxBackends: ["kubernetes", "kubernetes-granule"],
        sandboxBackendOptions: [
          {
            backend: "container",
            available: false,
            reason: "needs a container image and a git proxy host",
          },
          {
            backend: "granule",
            available: false,
            reason: "needs SSH user/workspace for granule",
          },
          {
            backend: "kubernetes",
            available: true,
          },
          {
            backend: "kubernetes-granule",
            available: true,
          },
          {
            backend: "host",
            available: false,
            reason: "needs -host-sandboxes at daemon startup",
          },
          {
            backend: "none",
            available: false,
            reason: "needs -disable-native-sandboxes at daemon startup",
          },
        ],
      },
      saveChoice: vi.fn(),
      onTextBlur: () => () => {},
      onIntBlur: () => () => {},
      onFloatBlur: () => () => {},
      onEnterKey: () => {},
      markDirty: () => {},
    };

    render(<SandboxesSection s={s} />);

    const containerOpt = screen.getByRole("option", {
      name: "Local Docker containers — unavailable (needs a container image and a git proxy host)",
    });
    expect(containerOpt).toBeDisabled();
    expect(containerOpt).toHaveAttribute(
      "title",
      "needs a container image and a git proxy host",
    );

    const granuleOpt = screen.getByRole("option", {
      name: "Granule virtual machines — unavailable (needs SSH user/workspace for granule)",
    });
    expect(granuleOpt).toBeDisabled();

    const k8sOpt = screen.getByRole("option", {
      name: "Kubernetes — container pod per run (no VM)",
    });
    expect(k8sOpt).not.toBeDisabled();

    const k8sGranuleOpt = screen.getByRole("option", {
      name: "Kubernetes — Granule VM per run",
    });
    expect(k8sGranuleOpt).not.toBeDisabled();

    expect(
      screen.getByText(
        /Each run executes in a Kubernetes container pod scheduled on the cluster, sharing the node kernel \(no VM\)\./i,
      ),
    ).toBeInTheDocument();
  });
  // The auto controller's bounds are read like every other number box
  // (numberBox.js): a value under the box's minimum, over its maximum, or
  // half-typed is refused by name and nothing is saved -- the box used to
  // put -1 back without a word, send 0.1 and 150 to the daemon, and save
  // grain's default for a "1e" the browser reports as "".
  describe("auto overcommit bounds", () => {
    const renderAuto = () => {
      const saveChoice = vi.fn();
      const refuseBox = vi.fn();
      const unchanged = vi.fn();
      render(
        <SandboxesSection
          s={{
            settings: {
              sandboxBackend: "granule",
              sandboxBackends: ["granule"],
              sandboxOvercommit: {
                mode: "auto",
                cpuMin: 1,
                cpuMax: 6,
                memoryMin: 1,
                memoryMax: 2,
                cpuPressureTarget: 25,
                memoryPressureTarget: 5,
              },
            },
            saveChoice,
            onTextBlur: () => () => {},
            onIntBlur: () => () => {},
            onFloatBlur: () => () => {},
            onEnterKey: () => {},
            markDirty: () => {},
            refuseBox,
            unchanged,
          }}
        />,
      );
      return { saveChoice, refuseBox, unchanged };
    };
    const box = (label) =>
      screen.getByRole("spinbutton", { name: label, exact: true });

    it.each([
      [
        "Auto CPU minimum",
        "-1",
        "sandboxOvercommitCpuMin",
        "Auto CPU minimum must be at least 0.25; -1 was not saved.",
      ],
      [
        "Auto CPU maximum",
        "0",
        "sandboxOvercommitCpuMax",
        "Auto CPU maximum must be at least 0.25; 0 was not saved.",
      ],
      [
        "Auto memory minimum",
        "0.1",
        "sandboxOvercommitMemoryMin",
        "Auto memory minimum must be at least 0.25; 0.1 was not saved.",
      ],
      [
        "CPU pressure target (%)",
        "150",
        "sandboxOvercommitCpuPressureTarget",
        "CPU pressure target (%) cannot be more than 100; 150 was not saved.",
      ],
    ])("refuses %s = %s by name", (label, typed, key, message) => {
      const { saveChoice, refuseBox } = renderAuto();
      const input = box(label);
      const before = input.value;
      fireEvent.change(input, { target: { value: typed } });
      fireEvent.blur(input);
      expect(refuseBox).toHaveBeenCalledWith(key, message);
      expect(saveChoice).not.toHaveBeenCalled();
      expect(input.value).toBe(before);
    });

    it("refuses a half-typed number rather than saving grain's default", () => {
      const { saveChoice, refuseBox } = renderAuto();
      const input = box("Auto CPU maximum");
      Object.defineProperty(input, "validity", {
        value: { badInput: true },
      });
      fireEvent.change(input, { target: { value: "" } });
      fireEvent.blur(input);
      expect(refuseBox).toHaveBeenCalledWith(
        "sandboxOvercommitCpuMax",
        "Auto CPU maximum: what was typed is not a number, so it was not saved.",
      );
      expect(saveChoice).not.toHaveBeenCalled();
    });

    it("puts a cleared box back to the default it stands for", () => {
      const { saveChoice, unchanged } = renderAuto();
      const input = box("Auto CPU minimum");
      fireEvent.change(input, { target: { value: "" } });
      fireEvent.blur(input);
      expect(saveChoice).not.toHaveBeenCalled();
      expect(unchanged).toHaveBeenCalledWith("sandboxOvercommitCpuMin");
      expect(input.value).toBe("1");
    });

    it("saves a value in range with the rest of the setting", () => {
      const { saveChoice, refuseBox } = renderAuto();
      const input = box("Memory pressure target (%)");
      fireEvent.change(input, { target: { value: "7.5" } });
      fireEvent.blur(input);
      expect(refuseBox).not.toHaveBeenCalled();
      expect(saveChoice).toHaveBeenCalledWith({
        sandboxOvercommit: {
          mode: "auto",
          cpuMin: 1,
          cpuMax: 6,
          memoryMin: 1,
          memoryMax: 2,
          cpuPressureTarget: 25,
          memoryPressureTarget: 7.5,
        },
      });
    });
  });
});
