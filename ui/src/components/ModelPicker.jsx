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

import {
  deploymentRow,
  modelOptions,
  connectionModels,
  teamDefaultLabel,
} from "../state.js";
import ModelSelect from "./ModelSelect.jsx";

// ModelPicker is the model a template or a schedule chooses for every
// task it files (ui.Template.Model, ui.Schedule.Model): one of the models
// the team's connections advertise -- the same choices, read the same way
// (connectionModels), as the New task form's own model picker -- or "" to leave
// it to whatever comes next: the template's own model for a schedule,
// and the deployment's default model otherwise. It names a model, never a
// connection: which connection serves the run is the rotation's to
// decide at dispatch (docs/connections-and-models.md).
export default function ModelPicker({
  config,
  value,
  onChange,
  emptyLabel,
  helperText,
  name = "model",
  margin = "normal",
}) {
  const models = connectionModels(config?.agentBackends);
  const deployment = deploymentRow(config);
  const empty = emptyLabel || teamDefaultLabel(deployment, models);
  return (
    <ModelSelect
      name={name}
      label="Model"
      value={value || ""}
      onChange={onChange}
      options={modelOptions(models, empty, value)}
      helperText={helperText}
      margin={margin}
    />
  );
}
