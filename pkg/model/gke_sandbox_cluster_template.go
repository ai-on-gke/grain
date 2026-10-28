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

package model

import "slices"

// GKESandboxClusterTemplateID names the built-in interactive template
// that walks a person through setting up an off-cluster GKE sandbox
// cluster and attaching it to this deployment: the gke-sandbox-cluster
// bootstrap playbook (pkg/capability/bootstrap/playbooks), driven as a
// live chat.
//
// Unlike every other system template it backs no schedule. It is filed
// by hand, from the template picker (FileableSystemTemplates), which is
// the only reason it is a template at all rather than a paragraph of
// docs: the inputs, the grants and the prompt arrive together, so the
// person filing it does not have to know which capabilities a cluster
// setup needs.
const GKESandboxClusterTemplateID = SystemTemplatePrefix + "gke-sandbox-cluster"

// FileableSystemTemplates is every template this build ships for a
// person to file a task from: listed in the template picker beside the
// deployment's own rows (ui.Client.ListTemplates), and resolved when a
// task is filed from one (ui.Client.fromTemplate).
//
// Kept apart from SystemTemplates, which is what the schedule seeder
// walks: nothing here is ever fired on a cadence, and a standing job is
// not something a person should be offered to file by hand -- the memory
// agent's grants are the memory agent's, not a picker entry's.
//
// Not editable per deployment, for SystemTemplate's own reason: what a
// built-in template tells an agent, and what it grants it, is grain's to
// say and is reviewed where grain's other prompts are. A deployment that
// wants a variant files its own template; a deployment that does not
// want the built-in one offered simply never picks it.
func FileableSystemTemplates() []Template {
	return []Template{*gkeSandboxClusterTemplate()}
}

// FileableSystemTemplate is FileableSystemTemplates' entry for id, or nil
// for an id that is not one -- including the system templates that back
// schedules, which a person is not offered to file.
func FileableSystemTemplate(id string) *Template {
	for _, t := range FileableSystemTemplates() {
		if t.ID == id {
			return &t
		}
	}
	return nil
}

// gkeSandboxClusterMachineTypes are the machine types the playbook
// describes, in the order it describes them: the high-density C4 shape
// and the smaller N2 ones.
var gkeSandboxClusterMachineTypes = []string{
	"c4-standard-96-lssd",
	"n2-standard-8",
	"n2-standard-16",
	"n2-standard-32",
}

// gkeSandboxClusterTemplate is the template itself. Each piece of it is
// a decision:
//
//   - Interactive, because every host command the playbook runs waits on
//     a person's live approval in the chat (selfrepair's
//     run_host_command), and "switch Settings -> Sandboxes" is a click
//     only that person can make.
//   - bootstrap-playbooks, for the runbook itself, and self-repair,
//     which is what actually runs `grain init` on this host: without it
//     the agent could read the steps and do none of them. Both
//     GrantByLabel, as if the person filing had ticked them: they did,
//     by picking this template, and a grant grain itself makes
//     (GrantByGrain) is for the jobs grain files on its own behalf.
//   - No Target. Nothing here is a change to a repository, so a task
//     filed from it with no repo of its own is filed with none
//     (ui.Client.fromTemplate).
//   - NetworkOpen, because the run talks to GCP through the host and the
//     shipped host-directory backend cannot honour NetworkNone anyway.
//   - Inputs for the four answers the playbook says to collect in one
//     message, each optional: a blank one is what the agent asks for
//     before its first command, rather than something the form refuses.
func gkeSandboxClusterTemplate() *Template {
	return &Template{
		ID:    GKESandboxClusterTemplateID,
		Name:  "Set up a GKE sandbox cluster",
		Title: "Set up a GKE sandbox cluster and attach it to grain",
		Body:  gkeSandboxClusterBody,
		Fields: []TemplateField{
			{Name: "project", Label: "GCP sandbox project ID"},
			{Name: "location", Label: "Zone or region", Default: "us-central1-a"},
			{Name: "cluster", Label: "Cluster name", Default: "grain-sandboxes"},
			{Name: "machine_type", Label: "Machine type",
				Options: slices.Clone(gkeSandboxClusterMachineTypes), Default: "n2-standard-8"},
		},
		NetworkAccess: NetworkOpen,
		Interactive:   true,
		Grants: []Grant{
			// Named as strings rather than through each capability
			// package's own CapabilityName: those packages import this
			// one.
			{Capability: "bootstrap-playbooks", Via: GrantByLabel},
			{Capability: "self-repair", Via: GrantByLabel},
		},
	}
}

const gkeSandboxClusterBody = "Walk me through setting up a GKE Standard sandbox cluster for this grain " +
	"deployment and attaching it, so its task sandboxes run as Granule VMs on GKE.\n\n" +
	"What I filled in when I filed this (a blank means I have not decided yet):\n\n" +
	"- GCP sandbox project: {{project}}\n" +
	"- Zone or region: {{location}}\n" +
	"- Cluster name: {{cluster}}\n" +
	"- Machine type: {{machine_type}}\n\n" +
	"Follow the `gke-sandbox-cluster` bootstrap playbook (read it with " +
	"read_bootstrap_playbook before you do anything else), in this order:\n\n" +
	"1. In one message, confirm the four values above, ask for any that are blank, and ask " +
	"for the node pool name and pool size the playbook also needs. Do not run anything " +
	"until I have answered.\n" +
	"2. Run the playbook's credential, API, quota and org-policy checks.\n" +
	"3. Run `grain init -platform=gke-sandboxes -dry-run` with those values and show me the " +
	"plan it prints.\n" +
	"4. Once I say go, run the same command without `-dry-run`, then the playbook's " +
	"verification steps.\n" +
	"5. Ask me to switch [Settings → Sandboxes](/settings/sandboxes) to `kubernetes-granule`, " +
	"with the namespace and kubeconfig the playbook gives, and help me confirm a smoke task's " +
	"sandbox lands on the cluster.\n\n" +
	"Every host command goes through run_host_command, which waits for my approval in this " +
	"chat: say in one line what you are about to run and why before each one. If a step " +
	"fails, stop and show me the output rather than working around it, and use the " +
	"playbook's rollback section if I ask to undo what was created.\n"
