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

package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// schemaLabelDockerfile is postSplitDockerfile as grain's own has been
// since it took the io.grain.schema-version label, which pkg/upgrade
// reads off a target's registry config to grey out a schema downgrade
// before pulling it.
const schemaLabelDockerfile = postSplitDockerfile +
	"ARG SCHEMA_VERSION=\nLABEL io.grain.schema-version=\"${SCHEMA_VERSION}\"\n"

const schemaLabelInspect = `image inspect --format {{index .Config.Labels "io.grain.schema-version"}} docker.io/grainai/grain:v1.5.0`

// schemaAnswers is stubAnswers with the rebuilt image answering its
// schema label with label.
func schemaAnswers(t *testing.T, short, label string) string {
	t.Helper()
	script := `case "$*" in
  "image inspect --format "*io.grain.schema-version*" docker.io/grainai/grain:v1.5.0") echo "` + label + `"; return 0 ;;
esac
. "` + stubAnswers(t, short, "", "v1.5.0") + `"
`
	path := filepath.Join(t.TempDir(), "schema-answers.sh")
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatalf("writing the stub's answers: %v", err)
	}
	return path
}

func schemaRelease(t *testing.T, schemaGo string) (string, map[string]string) {
	t.Helper()
	files := map[string]string{}
	if schemaGo != "" {
		files["pkg/model/schema.go"] = schemaGo
	}
	return releaseRepoWithFiles(t, schemaLabelDockerfile, true, files)
}

const schemaGo = "package model\n\n// SchemaVersion is the store's schema.\nconst SchemaVersion = 146\n"

// The promotion rebuild stamps the release's own pkg/model SchemaVersion
// as io.grain.schema-version, and reads the label back before publishing.
func TestPromoteRebuildStampsTheReleasesSchemaVersion(t *testing.T) {
	remote, commits := schemaRelease(t, schemaGo)
	short := commits["released"][:7]
	out, docker, err := promoteRun(t, remote, []string{"promote"}, publishEnv(schemaAnswers(t, short, "146")))
	if err != nil {
		t.Fatalf("promote failed: %v\n%s\ndocker was asked:\n%s", err, out, docker)
	}
	containsRun(t, docker, "--build-arg GIT_REVISION="+commits["released"]+" --build-arg SCHEMA_VERSION=146 ")
	containsRun(t, docker, schemaLabelInspect)
	containsRun(t, docker, "push docker.io/grainai/grain:v1.5.0")
}

// A rebuild whose image does not carry the schema its tree names is not
// published: a release with an empty label leaves every deployment to
// pull it before it can tell it would downgrade the store.
func TestPromoteOfARebuildMislabellingItsSchemaMovesNothing(t *testing.T) {
	remote, commits := schemaRelease(t, schemaGo)
	short := commits["released"][:7]
	out, docker, err := promoteRun(t, remote, []string{"promote"}, publishEnv(schemaAnswers(t, short, "")))
	if err == nil {
		t.Fatalf("promote of a rebuild with no schema label succeeded:\n%s\ndocker was asked:\n%s", out, docker)
	}
	containsRun(t, out, "is labelled with schema nothing, not the 146 its tree is built for")
	if strings.Contains(docker, "push docker.io/grainai/grain:v1.5.0") {
		t.Errorf("promote pushed a rebuild with no schema label:\n%s", docker)
	}
}

// A release whose Dockerfile asks for the label but whose schema cannot
// be read is refused before it is built.
func TestPromoteOfAReleaseWithAnUnreadableSchemaBuildsNothing(t *testing.T) {
	remote, commits := schemaRelease(t, "")
	short := commits["released"][:7]
	out, docker, err := promoteRun(t, remote, []string{"promote"}, publishEnv(schemaAnswers(t, short, "146")))
	if err == nil {
		t.Fatalf("promote of a release with no readable schema succeeded:\n%s\ndocker was asked:\n%s", out, docker)
	}
	containsRun(t, out, "no `const SchemaVersion = <n>` was found")
	if strings.Contains(docker, "build ") {
		t.Errorf("promote built a release whose schema it could not read:\n%s", docker)
	}
}

// A release from before the label (its Dockerfile declares no ARG
// SCHEMA_VERSION) is rebuilt as it was, with no schema to stamp or check.
func TestPromoteOfAReleaseBeforeTheSchemaLabelPassesNoSchema(t *testing.T) {
	remote, commits := releaseRepoWithFiles(t, postSplitDockerfile, true, map[string]string{
		"pkg/model/schema.go": schemaGo,
	})
	short := commits["released"][:7]
	out, docker, err := promoteRun(t, remote, []string{"promote"}, publishEnv(stubAnswers(t, short, "", "v1.5.0")))
	if err != nil {
		t.Fatalf("promote failed: %v\n%s\ndocker was asked:\n%s", err, out, docker)
	}
	if strings.Contains(docker, "SCHEMA_VERSION") || strings.Contains(docker, "io.grain.schema-version") {
		t.Errorf("promote of a release predating the schema label passed or checked one:\n%s", docker)
	}
	containsRun(t, docker, "push docker.io/grainai/grain:v1.5.0")
}
