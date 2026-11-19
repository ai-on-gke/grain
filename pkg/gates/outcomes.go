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

package gates

import (
	"encoding/json"
	"time"
)

// ResourceFact is a scoped internal observation, never an agent's claimed URL.
// Version covers identity, head, policy, attribution and the provider receipt.
type ResourceFact struct {
	Evidence   Evidence
	State      string
	Complete   bool
	ObservedAt time.Time
	ValidUntil time.Time
}
type ChildFact struct {
	LegacyMerged bool
	SuccessKnown bool
	MergedKnown  bool
	Evidence     Evidence
	Known        bool
	Closed       bool
	Merged       bool
	Success      bool
	Failed       bool
}

func evaluateDelivery(c DeliveryConfig, f Facts, r Result) Result {
	switch {
	case f.Running:
		r.Status = "pending"
		r.Summary = "Product producer is still running."
	case f.Blocked:
		r.Status = "pending"
		r.Summary = "Question, access request or suspension takes finish precedence."
	case f.ProductUnknown != "":
		r.Summary = f.ProductUnknown
	case !f.Settled:
		r.Status = "unmet"
		r.Summary = "No current settled product is recorded."
	case f.ProductEvidence != nil && (c.Product == "any" || c.Product == f.Product):
		r.Status = "met"
		r.Summary = "Settled run delivered " + f.Product + "; this predicts delivery only."
		r.Evidence = []Evidence{*f.ProductEvidence}
	default:
		r.Status = "unmet"
		r.Summary = "The selected product was not delivered under finish precedence."
	}
	return r
}
func evaluateLegacyAck(c AcknowledgmentConfig, f Facts, r Result) Result {
	if !f.Settled || f.Running || f.Blocked {
		r.Summary = "No current settled product for compatibility acknowledgment."
		return r
	}
	if f.ProductUnknown != "" {
		r.Summary = f.ProductUnknown
		return r
	}
	switch c.Mode {
	case "legacy_report":
		if f.Report != nil && f.Product == "report" {
			r.Evidence = append(r.Evidence, *f.Report)
			switch {
			case f.Receipt != nil:
				r.Status = "met"
				r.Evidence = append(r.Evidence, *f.Receipt)
				r.Summary = "Legacy report acknowledgment has an eligible person's receipt."
			case f.ReceiptUnversioned:
				r.Summary = "Legacy report closure lacks current eligible receipt provenance."
			default:
				r.Status = "pending"
				r.Summary = "Legacy report policy awaits a person's acknowledgment."
			}
			return r
		}
	case "legacy_child_answer":
		if f.ChildAnswer && f.Report != nil && f.Product == "report" {
			r.Evidence = append(r.Evidence, *f.Report)
			r.Status = "met"
			r.Summary = "Report-only answer with original agent filing provenance."
			return r
		}
	case "legacy_nothing_needed_ack":
		if f.NothingAck && f.Product == "nothing_needed" {
			if f.NoWorkReceipt != nil {
				r.Status = "met"
				r.Evidence = append(r.Evidence, *f.NoWorkReceipt)
				r.Summary = "Configured legacy no-work acknowledgment recorded."
			} else if f.ReceiptUnversioned {
				r.Summary = "No-work closure lacks current eligible receipt provenance."
			} else {
				r.Status = "pending"
				r.Summary = "Configured no-work policy awaits a person's acknowledgment."
			}
			return r
		}
	}
	r.Summary = "Compatibility mode lacks applicable product or filing provenance."
	return r
}
func evaluateIntegration(b Binding, f Facts, r Result) Result {
	var c IntegrationConfig
	_ = json.Unmarshal(b.Config, &c)
	resources, ok := f.Resources[b.ID]
	if !ok {
		resources = f.Resources[c.Kind]
	}
	if len(resources) == 0 {
		r.Summary = "Complete scoped change identities and integration observations are unavailable."
		return r
	}
	r.Evidence = []Evidence{}
	r.Dependencies[0].Key = "task/" + f.TaskID + "/" + b.ID
	r.Dependencies[0].ScopeDigest = Digest(struct {
		Subject Subject
		Config  json.RawMessage
	}{b.Subject, b.Config})
	r.Dependencies[0].Complete = true
	pending, unmet := false, false
	for _, resource := range resources {
		dependency := r.Dependencies[0]
		dependency.Key = "change/" + resource.Evidence.ID
		dependency.Subject = resource.Evidence.Subject
		dependency.Head = resource.Evidence.Head
		dependency.Version = resource.Evidence.Version
		dependency.ObservedAt = resource.ObservedAt
		dependency.ValidUntil = resource.ValidUntil
		dependency.Complete = resource.Complete
		r.Dependencies = append(r.Dependencies, dependency)
		if resource.ObservedAt.After(r.ObservedAt) || !resource.Complete || resource.Evidence.Head == nil || *resource.Evidence.Head == "" || resource.Evidence.Version == "" || !r.ObservedAt.Before(resource.ValidUntil) {
			r.Dependencies[0].Complete = false
			r.Summary = "Change identity, exact head/policy or complete fresh observation is unavailable."
			return r
		}
		if resource.ValidUntil.Before(r.ValidUntil) {
			r.ValidUntil = resource.ValidUntil
		}
		r.Evidence = append(r.Evidence, resource.Evidence)
		switch resource.State {
		case "merged", "submitted", "equivalent":
		case "submitting":
			pending = true
		case "open", "closed", "abandoned":
			unmet = true
		default:
			r.Summary = "Integration outcome is unknown."
			return r
		}
	}
	switch {
	case unmet:
		r.Status = "unmet"
		r.Summary = "A scoped change is open or closed without integration."
	case pending:
		r.Status = "pending"
		r.Summary = "A scoped submission is in flight."
	default:
		r.Status = "met"
		r.Summary = "Every scoped change has positive integration proof; attribution is retained in evidence."
	}
	return r
}
func evaluateChildren(b Binding, f Facts, r Result) Result {
	var c ChildrenConfig
	_ = json.Unmarshal(b.Config, &c)
	if f.Cycle {
		r.Summary = "A cycle or incomplete bounded outcome relation graph prevents prediction; actual legacy waits are unchanged."
		return r
	}
	r.Evidence = []Evidence{}
	r.Dependencies[0].Key = "task/" + f.TaskID + "/" + b.ID
	r.Dependencies[0].ScopeDigest = Digest(struct {
		Subject Subject
		Config  json.RawMessage
	}{b.Subject, b.Config})
	r.Dependencies[0].Complete = true
	pending, unmet := false, false
	for _, id := range c.Tasks {
		child, ok := f.Children[id]
		if !ok || !child.Known {
			r.Dependencies[0].Complete = false
			r.Summary = "A selected child is missing, inaccessible or has unknown outcome provenance."
			return r
		}
		dependency := r.Dependencies[0]
		dependency.Key = "task/" + id + "/outcome"
		dependency.Subject = child.Evidence.Subject
		dependency.Version = child.Evidence.Version
		r.Dependencies = append(r.Dependencies, dependency)
		r.Evidence = append(r.Evidence, child.Evidence)
		if c.Predicate == "success" && child.Closed && !child.SuccessKnown || c.Predicate == "merged" && child.Closed && !child.MergedKnown {
			r.Summary = "Selected child has no trustworthy proof for the requested outcome."
			return r
		}
		met := c.Predicate == "closed" && child.Closed || c.Predicate == "merged" && child.Merged || c.Predicate == "success" && child.Success
		if !met {
			if child.Closed || c.Predicate == "success" && child.Failed {
				unmet = true
			} else {
				pending = true
			}
		}
	}
	switch {
	case unmet:
		r.Status = "unmet"
		r.Summary = "A selected child ended without the required " + c.Predicate + " outcome."
	case pending:
		r.Status = "pending"
		r.Summary = "Selected children await the explicit " + c.Predicate + " predicate."
	default:
		r.Status = "met"
		r.Summary = "All explicitly selected children prove " + c.Predicate + "; closure is not relabeled success."
	}
	return r
}
