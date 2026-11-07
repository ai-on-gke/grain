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

// Package skill holds the embedded static template for `grain skill install`
// (design/spec/external-cli/external-cli.spec.md#XC-30).
package skill

import (
	"bytes"
	_ "embed"
	"text/template"
)

//go:embed SKILL.md.tmpl
var rawTemplate string

var parsed = template.Must(template.New("SKILL.md").Option("missingkey=error").Parse(rawTemplate))

// Data is the template input for rendering SKILL.md.
type Data struct {
	MetadataJSON      string
	ContextFlag       string
	DeploymentSection string
}

// Render executes SKILL.md.tmpl with d.
func Render(d Data) (string, error) {
	var buf bytes.Buffer
	if err := parsed.Execute(&buf, d); err != nil {
		return "", err
	}
	return buf.String(), nil
}
