import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import ModelDefaults from "./ModelDefaults.jsx";

describe("ModelDefaults", () => {
  // grain/task-4527: with no default model the panel asks for one, offers
  // the discovered list, and labels what runs meanwhile as the provider
  // default -- without saving anything itself.
  it("asks for a default model when none is set, and saves nothing on its own", () => {
    const onSave = vi.fn();
    const connections = [
      {
        id: "agy",
        name: "Gemini key",
        framework: "antigravity",
        discovered: { models: ["gemini-3.8-flash", "gemini-3.1-pro"] },
      },
      {
        id: "house",
        name: "House agent",
        framework: "custom",
      },
    ];
    render(
      <ModelDefaults
        deployment={{ id: "default" }}
        connections={connections}
        onSave={onSave}
      />,
    );
    const prompt = screen.getByTestId("pick-default-model");
    expect(prompt).toHaveTextContent("Pick a default model.");
    expect(prompt).toHaveTextContent("provider default (unset)");
    expect(prompt).toHaveTextContent("Gemini key: auto");
    expect(prompt).toHaveTextContent("Choose one of the 2 models");
    expect(screen.getByTestId("no-models")).toHaveTextContent(
      "House agent: no models — set one.",
    );
    expect(screen.getByTestId("model-gemini-3.8-flash")).toBeInTheDocument();
    expect(screen.getByTestId("model-gemini-3.1-pro")).toBeInTheDocument();
    expect(onSave).not.toHaveBeenCalled();
  });

  it("drops the prompt once a default is picked", () => {
    render(
      <ModelDefaults
        deployment={{ id: "default", defaultModel: "gemini-3.1-pro" }}
        connections={[
          {
            id: "agy",
            framework: "antigravity",
            discovered: { models: ["gemini-3.1-pro"] },
          },
        ]}
        onSave={vi.fn()}
      />,
    );
    expect(screen.queryByTestId("pick-default-model")).not.toBeInTheDocument();
  });

  // grain/task-1937: "Served by" named a paused connection as
  // "(Enabled)", the same as one dispatch was using.
  it("names a connection whose credential is paused in Served by", () => {
    const connections = [
      {
        id: "ag",
        name: "gemini 2.5 pro",
        framework: "antigravity",
        models: ["gemini-2.5-pro"],
      },
    ];
    const pause = {
      paused: true,
      until: "2026-10-04T22:58:16Z",
      secondsRemaining: 3600,
      reason: "quota",
      pauses: [
        {
          connection: "ag",
          framework: "antigravity",
          until: "2026-10-04T22:58:16Z",
          secondsRemaining: 3600,
          reason: "quota",
        },
      ],
    };
    const { rerender } = render(
      <ModelDefaults
        deployment={{}}
        connections={connections}
        pause={pause}
        onSave={vi.fn()}
      />,
    );
    const chip = screen.getByText("gemini 2.5 pro (Paused)");
    expect(chip.closest(".MuiChip-root")).toHaveAttribute(
      "title",
      expect.stringMatching(/paused until .* — quota/),
    );

    rerender(
      <ModelDefaults
        deployment={{}}
        connections={connections}
        onSave={vi.fn()}
      />,
    );
    expect(screen.getByText("gemini 2.5 pro (Enabled)")).toBeInTheDocument();
  });

  // grain/task-3100: the panel is part of the merged Models &
  // connections tab, so its warnings stay and its empty state points at
  // the connections above it (grain/task-3702) rather than at a tab that
  // is gone.
  it("warns of a default model only disabled connections serve, and of one none advertises", () => {
    const connections = [
      {
        id: "off",
        name: "old key",
        framework: "antigravity",
        models: ["gemini-2.5-pro"],
        disabled: true,
      },
    ];
    const { rerender } = render(
      <ModelDefaults
        deployment={{ defaultModel: "gemini-2.5-pro" }}
        connections={connections}
        onSave={vi.fn()}
      />,
    );
    expect(
      screen.getByText(/No enabled connection serves model/),
    ).toBeInTheDocument();

    rerender(
      <ModelDefaults
        deployment={{ defaultModel: "claude-gone" }}
        connections={connections}
        onSave={vi.fn()}
      />,
    );
    expect(
      screen.getByText(/No enabled connection serves model/),
    ).toHaveTextContent("claude-gone");
    expect(screen.getByText("claude-gone")).toBeInTheDocument();
  });

  it("points at the connections above when none advertises a model", () => {
    render(<ModelDefaults deployment={{}} connections={[]} onSave={vi.fn()} />);
    expect(
      screen.getByText(/advertises a model yet. Add a connection above/),
    ).toBeInTheDocument();
    expect(screen.queryByText(/Connections tab/)).toBeNull();
  });

  // grain/task-3683: the pane named the team by its id; it names the
  // deployment now.
  it("names the deployment, not a team", () => {
    render(
      <ModelDefaults
        deployment={{ id: "default" }}
        connections={[]}
        onSave={vi.fn()}
      />,
    );
    expect(
      screen.getByText(/Choose the model this deployment runs on/),
    ).toBeInTheDocument();
  });

  it("shows fallback default models in order and supports reordering and removing", () => {
    const onSave = vi.fn();
    const connections = [
      {
        id: "agy",
        name: "Gemini key",
        framework: "antigravity",
        models: ["gemini-3.1-pro", "gemini-2.5-flash", "claude-opus-5"],
      },
    ];
    render(
      <ModelDefaults
        deployment={{
          id: "default",
          defaultModel: "gemini-3.1-pro,gemini-2.5-flash",
          defaultModels: ["gemini-3.1-pro", "gemini-2.5-flash"],
        }}
        connections={connections}
        onSave={onSave}
      />,
    );
    expect(screen.getByLabelText("Default model")).toHaveValue(
      "gemini-3.1-pro",
    );
    expect(screen.getByLabelText("Fallback model")).toHaveValue(
      "gemini-2.5-flash",
    );
    expect(screen.getByLabelText("Add fallback model")).toBeInTheDocument();
    expect(screen.getByText("Default")).toBeInTheDocument();
    expect(screen.getByText("Default #2")).toBeInTheDocument();

    screen.getByRole("button", { name: "Move gemini-2.5-flash up" }).click();
    expect(onSave).toHaveBeenCalledWith({
      defaultModel: "gemini-2.5-flash,gemini-3.1-pro",
    });

    onSave.mockClear();
    screen.getByRole("button", { name: "Remove gemini-2.5-flash" }).click();
    expect(onSave).toHaveBeenCalledWith({
      defaultModel: "gemini-3.1-pro",
    });
  });
});
