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

// Package attest is grit's proof of work: the in-toto Statement and run
// predicate grit signs when a run concludes, the DSSE envelope it is
// carried in, and the signing and verification both grain and
// `grit verify` use (docs/design/grit-split.md, "Attestations").
//
// It is a neutral package: it imports nothing of grain's, so grit and
// any third party verifying a release's tests can use it alone.
package attest

import (
	"encoding/json"
	"fmt"
	"time"
)

const (
	// StatementType is the in-toto Statement v1 type.
	StatementType = "https://in-toto.io/Statement/v1"
	// PayloadType is the DSSE payload type of an in-toto Statement.
	PayloadType = "application/vnd.in-toto+json"
	// RunPredicateType names RunPredicate.
	RunPredicateType = "https://grain.dev/grit/run/v1"
)

// Subject names are fixed: the tree tested, and the commit when the run
// named one.
const (
	SubjectTree   = "tree"
	SubjectCommit = "commit"
)

// Statement is an in-toto Statement v1 whose predicate is a grit run.
type Statement struct {
	Type          string       `json:"_type"`
	Subject       []Subject    `json:"subject"`
	PredicateType string       `json:"predicateType"`
	Predicate     RunPredicate `json:"predicate"`
}

// Subject is one artifact the statement is about, by digest only.
type Subject struct {
	Name   string            `json:"name"`
	Digest map[string]string `json:"digest"`
}

// Tree is a tree's identity (pkg/treeid's ID, as the wire spells it):
// the sha256 of its canonical archive and its git tree object id.
type Tree struct {
	SHA256  string `json:"sha256"`
	GitTree string `json:"gitTree"`
}

// NewStatement is a statement about tree and, when it is not empty,
// commit, carrying p.
func NewStatement(tree Tree, commit string, p RunPredicate) Statement {
	s := Statement{
		Type:          StatementType,
		Subject:       []Subject{{Name: SubjectTree, Digest: map[string]string{"sha256": tree.SHA256, "gitTree": tree.GitTree}}},
		PredicateType: RunPredicateType,
		Predicate:     p,
	}
	if commit != "" {
		s.Subject = append(s.Subject, Subject{Name: SubjectCommit, Digest: map[string]string{"gitCommit": commit}})
	}
	return s
}

// Tree is the tree subject's identity, zero when there is none.
func (s Statement) Tree() Tree {
	for _, sub := range s.Subject {
		if sub.Name == SubjectTree {
			return Tree{SHA256: sub.Digest["sha256"], GitTree: sub.Digest["gitTree"]}
		}
	}
	return Tree{}
}

// Commit is the commit subject's id, empty when the run named none.
func (s Statement) Commit() string {
	for _, sub := range s.Subject {
		if sub.Name == SubjectCommit {
			return sub.Digest["gitCommit"]
		}
	}
	return ""
}

// CheckSubject reports whether the statement is about exactly tree and
// commit: the second of grain's checks ("Grain verifies before anything
// counts"). A statement about any other tree is refused, whatever its
// signature, so a verdict cannot be moved from one tree to another.
func (s Statement) CheckSubject(tree Tree, commit string) error {
	got := s.Tree()
	if tree.SHA256 == "" || tree.GitTree == "" {
		return fmt.Errorf("attest: the tree to check against has no digest")
	}
	if got != tree {
		return fmt.Errorf("attest: statement is about tree %s (git %s), not %s (git %s)", got.SHA256, got.GitTree, tree.SHA256, tree.GitTree)
	}
	if c := s.Commit(); c != commit {
		return fmt.Errorf("attest: statement names commit %q, not %q", c, commit)
	}
	return nil
}

// RunPredicate is what a grit run did, as the design doc's *Format*
// spells it. Secrets appear by name and version only, never by value.
type RunPredicate struct {
	Grit      GritInfo     `json:"grit"`
	Client    ClientInfo   `json:"client"`
	Request   RequestInfo  `json:"request"`
	Workflows []FileDigest `json:"workflows"`
	Jobs      []Job        `json:"jobs"`
	Timing    Timing       `json:"timing"`
	IssuedAt  time.Time    `json:"issuedAt"`
}

// GritInfo names the grit build and instance that ran the work.
type GritInfo struct {
	Version  string `json:"version"`
	Build    string `json:"build,omitempty"`
	Instance string `json:"instance,omitempty"`
	GritPod  string `json:"gritPod,omitempty"`
}

// ClientInfo is the requesting client, with the labels it sent, which
// grit copies here and never interprets.
type ClientInfo struct {
	ID        string            `json:"id"`
	Principal string            `json:"principal,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
}

// RequestInfo is the request the run answered.
type RequestInfo struct {
	RequestID   string    `json:"requestId"`
	Run         string    `json:"run"`
	Repo        string    `json:"repo"`
	Ref         string    `json:"ref,omitempty"`
	Event       string    `json:"event,omitempty"`
	BaseTree    *Tree     `json:"baseTree,omitempty"`
	BaseCommit  string    `json:"baseCommit,omitempty"`
	SubmittedAt time.Time `json:"submittedAt"`
}

// FileDigest is a file by path and content digest.
type FileDigest struct {
	Path   string `json:"path,omitempty"`
	Name   string `json:"name,omitempty"`
	SHA256 string `json:"sha256"`
}

// Job is one job of the run.
type Job struct {
	ID               string            `json:"id"`
	DefinitionSHA256 string            `json:"definitionSha256,omitempty"`
	Fingerprint      string            `json:"fingerprint,omitempty"`
	Image            *Image            `json:"image,omitempty"`
	Sandbox          *Sandbox          `json:"sandbox,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
	Secrets          []SecretRef       `json:"secrets,omitempty"`
	ModelCalls       []ModelCalls      `json:"modelCalls,omitempty"`
	Steps            []Step            `json:"steps,omitempty"`
	Result           Result            `json:"result"`
	Tests            *Tests            `json:"tests,omitempty"`
	Artifacts        []FileDigest      `json:"artifacts,omitempty"`
	Timing           *Timing           `json:"timing,omitempty"`
	// Cached is set, and Steps, Artifacts and Timing are not, for a job
	// whose verdict came from the job cache: it names the attestation
	// of the run that earned it rather than claiming a fresh run.
	Cached *CachedRef `json:"cached"`
}

// Image is the job image by reference and digest.
type Image struct {
	Ref    string `json:"ref"`
	Digest string `json:"digest,omitempty"`
}

// Sandbox is the shape the job ran on.
type Sandbox struct {
	CPUs          int    `json:"cpus,omitempty"`
	MemoryMiB     int    `json:"memoryMiB,omitempty"`
	NetworkAccess string `json:"networkAccess,omitempty"`
}

// SecretRef is a secret given to a job: its name and version, and the
// step it was scoped to (empty for the whole job). Never its value.
type SecretRef struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Step    string `json:"step"`
}

// ModelCalls counts the model requests the relay forwarded for a job.
type ModelCalls struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Requests int    `json:"requests"`
}

// Step is one step, by the digest of the exact script that ran.
type Step struct {
	Name          string    `json:"name"`
	CommandSHA256 string    `json:"commandSha256"`
	Exit          int       `json:"exit"`
	StartedAt     time.Time `json:"startedAt"`
	FinishedAt    time.Time `json:"finishedAt"`
	LogSHA256     string    `json:"logSha256,omitempty"`
}

// Result is a job's conclusion.
type Result struct {
	Conclusion     string `json:"conclusion"`
	Attempts       int    `json:"attempts"`
	Infrastructure bool   `json:"infrastructure"`
	Flake          bool   `json:"flake"`
	Quarantined    bool   `json:"quarantined"`
}

// Tests is a job's parsed test report.
type Tests struct {
	Passed        int      `json:"passed"`
	Failed        int      `json:"failed"`
	Skipped       int      `json:"skipped"`
	FailedNames   []string `json:"failedNames"`
	NamesSHA256   string   `json:"namesSha256,omitempty"`
	NamesArtifact string   `json:"namesArtifact,omitempty"`
}

// Timing is when something was queued, started, submitted and finished.
type Timing struct {
	QueuedAt    *time.Time `json:"queuedAt,omitempty"`
	SubmittedAt *time.Time `json:"submittedAt,omitempty"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
}

// CachedRef names the original attestation behind a cached verdict.
type CachedRef struct {
	// Attestation is "sha256:<hex>" of the original envelope (Digest).
	Attestation string `json:"attestation"`
	Run         string `json:"run"`
	Fingerprint string `json:"fingerprint"`
}

func (s Statement) marshal() ([]byte, error) { return json.Marshal(s) }
