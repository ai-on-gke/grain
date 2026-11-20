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

import { TestBed } from "@angular/core/testing";
import { afterEach, describe, expect, it } from "vitest";
import { GrainApi } from "../api/grain-api.service";
import {
  SettingsStateService,
  failedSaveLabels,
  readableSaveError,
} from "./settings-state.service";

describe("SettingsStateService.setDeployField", () => {
  afterEach(() => TestBed.resetTestingModule());

  function setup(deployment: Record<string, unknown> = {}) {
    let savedBody: unknown;
    const fakeApi = {
      config: () =>
        Promise.resolve({
          version: "v3.6.0",
          access: { role: "grain/operator", full: true, scopes: [] },
        }),
      get: () => Promise.resolve({}),
      put: (_key: string, _params?: unknown, body?: unknown) => {
        savedBody = body;
        // PUT /api/deployment returns Team with omitempty slices omitted when empty.
        return Promise.resolve({ id: "default" });
      },
      post: () => Promise.resolve({}),
    };
    TestBed.configureTestingModule({
      providers: [{ provide: GrainApi, useValue: fakeApi }],
    });
    const state = TestBed.inject(SettingsStateService);
    state.deployment.set(deployment);
    return { state, getSavedBody: () => savedBody };
  }

  it("treats an omitted or null deployment slice as [] so empty arrays leave no stuck draft", () => {
    const { state } = setup({ id: "default" });

    // Clicking Model default on an unset model produces [] while deployment["modelSettings"] is undefined.
    state.setDeployField("modelSettings", []);
    expect(state.deployDraft()).toEqual({});
    expect(state.isDirty()).toBe(false);
    expect(state.dirtyCount()).toBe(0);

    // Staging an effort marks the draft dirty; reverting to [] clears it again.
    state.setDeployField("modelSettings", [
      { model: "gemini-pro", effort: "medium" },
    ]);
    expect(state.isDirty()).toBe(true);
    expect(state.dirtyCount()).toBe(1);

    state.setDeployField("modelSettings", []);
    expect(state.deployDraft()).toEqual({});
    expect(state.isDirty()).toBe(false);
    expect(state.dirtyCount()).toBe(0);

    // Explicit null behaves the same as undefined.
    state.deployment.set({ id: "default", modelSettings: null });
    state.setDeployField("modelSettings", []);
    expect(state.isDirty()).toBe(false);
  });

  it("still stages [] when clearing a non-empty stored slice, and stays clean after save", async () => {
    const { state, getSavedBody } = setup({
      id: "default",
      modelSettings: [{ model: "gemini-pro", effort: "high" }],
    });
    await state.loadCore();
    state.deployment.set({
      id: "default",
      modelSettings: [{ model: "gemini-pro", effort: "high" }],
    });

    state.setDeployField("modelSettings", []);
    expect(state.deployDraft()).toEqual({ modelSettings: [] });
    expect(state.isDirty()).toBe(true);

    await state.saveChanges();
    expect(getSavedBody()).toEqual({ modelSettings: [] });
    expect(state.isDirty()).toBe(false);

    // Response omitted modelSettings via omitempty; another [] stays clean.
    state.setDeployField("modelSettings", []);
    expect(state.isDirty()).toBe(false);
  });
});

describe("failed save labels", () => {
  it("names a refused setting by its label in the error", () => {
    expect(
      readableSaveError(
        "taskBranchTemplate branch template must contain {id}",
        ["taskBranchTemplate"],
      ),
    ).toBe("Branch name template: branch template must contain {id}");
    expect(
      readableSaveError("temporarily unavailable", ["maxAgentTurns"]),
    ).toBe("temporarily unavailable");
  });

  it("lists what a failed save was carrying", () => {
    expect(
      failedSaveLabels(["taskBranchTemplate", "maxAgentTurns"], ["name"], true),
    ).toEqual([
      "Branch name template",
      "Max agent turns",
      "Deployment identity",
      "Deployment memory",
    ]);
    expect(failedSaveLabels([], [], false)).toEqual([]);
  });
});
