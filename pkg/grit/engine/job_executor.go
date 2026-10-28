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

package engine

import (
	"fmt"
	"strings"

	"github.com/ai-on-gke/grain/pkg/workflows"
)

// MaxJobRetries is the maximum number of automatic retry attempts allowed
// for a single CI job. Bounded strictly to 1 to protect CI runner capacity.
const MaxJobRetries = 1

// transientErrorSubstrings are signatures of transient infrastructure or
// runner host network/resource contention failures that qualify for automatic in-flight retry.
var transientErrorSubstrings = []string{
	"socket timeout",
	"connection reset",
	"resource contention",
	"runner host resource contention",
	"connection refused",
	"i/o timeout",
	"network is unreachable",
	"broken pipe",
	"temporary failure in name resolution",
	"no route to host",
	"tls handshake timeout",
	"client.timeout exceeded",
	"connection timed out",
	"server closed idle connection",
	"unexpected eof",
	"the exec session ended before the command reported an exit status",
	"flaky",
	"intermittent",
	"not reachable",
}

// IsDaemonServiceError reports whether text describes a failure to reach a
// daemon-hosted local service (the git proxy on 127.0.0.1:9728 or the Actions
// cache on 127.0.0.1:9758 / _apis/artifactcache/) that goes away while the
// daemon is shutting down.
func IsDaemonServiceError(text string) bool {
	if text == "" {
		return false
	}
	lower := strings.ToLower(text)
	if strings.Contains(lower, "this daemon runs no git proxy") {
		return true
	}
	if strings.Contains(lower, "127.0.0.1:9728") || strings.Contains(lower, "localhost:9728") {
		if strings.Contains(lower, "connection reset") ||
			strings.Contains(lower, "connection refused") ||
			strings.Contains(lower, "not reachable") ||
			strings.Contains(lower, "eof") ||
			strings.Contains(lower, "unable to access") ||
			strings.Contains(lower, "failed to connect") {
			return true
		}
	}
	if strings.Contains(lower, "actions cache") || strings.Contains(lower, "_apis/artifactcache") {
		if strings.Contains(lower, "not reachable") ||
			strings.Contains(lower, "connection reset") ||
			strings.Contains(lower, "connection refused") ||
			strings.Contains(lower, "eof") {
			return true
		}
	}
	return false
}

// JobFailedOnDaemonService reports whether result failed or errored with a
// daemon-service error in its job error, step error, or step output.
func JobFailedOnDaemonService(result workflows.JobRun) bool {
	if IsDaemonServiceError(result.Error) {
		return true
	}
	for _, step := range result.Steps {
		if IsDaemonServiceError(step.Error) || IsDaemonServiceError(step.Output) {
			return true
		}
	}
	for _, att := range result.Attempts {
		if IsDaemonServiceError(att.Error) {
			return true
		}
		for _, step := range att.Steps {
			if IsDaemonServiceError(step.Error) || IsDaemonServiceError(step.Output) {
				return true
			}
		}
	}
	return false
}

// IsTransientOrInfrastructureError reports whether text matches known
// transient socket/network timeouts, connection resets, or runner contention patterns.
func IsTransientOrInfrastructureError(text string) bool {
	lower := strings.ToLower(text)
	for _, sub := range transientErrorSubstrings {
		if strings.Contains(lower, sub) {
			return true
		}
	}
	// Check for EOF or deadline exceeded in network/transport context
	if strings.Contains(lower, "deadline exceeded") || strings.Contains(lower, ": eof") || strings.HasSuffix(lower, "eof") {
		return true
	}
	return false
}

// IsJobRetryEligible determines whether a failed job should be automatically
// re-attempted in a fresh sandbox.
func IsJobRetryEligible(job workflows.Job, result workflows.JobRun, attempt int) bool {
	if attempt >= MaxJobRetries {
		return false
	}
	if result.Conclusion == workflows.ConclusionSuccess || result.Conclusion == workflows.ConclusionCancelled || result.Conclusion == workflows.ConclusionSkipped {
		return false
	}
	// Designated retry policy on the job
	if job.Retry || job.Retries > 0 || job.RetryPolicy != "" {
		return true
	}
	// Infrastructure-classified failures
	if result.Conclusion == workflows.ConclusionErrored {
		return true
	}
	if IsTransientOrInfrastructureError(result.Error) {
		return true
	}
	for _, step := range result.Steps {
		if step.Conclusion != workflows.ConclusionSuccess {
			if IsTransientOrInfrastructureError(step.Error) || IsTransientOrInfrastructureError(step.Output) {
				return true
			}
		}
	}
	// Also check if any step in Job definition is marked as Infrastructure
	for _, step := range job.Steps {
		if step.Infrastructure {
			for _, stepRun := range result.Steps {
				if stepRun.Name == step.DisplayName() && stepRun.Conclusion != workflows.ConclusionSuccess {
					return true
				}
			}
		}
	}
	return false
}

// SummarizeFailure produces a short description of the failure for logging and flake warnings.
func SummarizeFailure(result workflows.JobRun) string {
	for _, step := range result.Steps {
		if step.Conclusion != workflows.ConclusionSuccess && step.Conclusion != workflows.ConclusionSkipped {
			if step.Error != "" {
				return fmt.Sprintf("step %q: %s", step.Name, step.Error)
			}
			if step.Output != "" {
				lines := strings.Split(strings.TrimSpace(step.Output), "\n")
				tail := lines[len(lines)-1]
				if len(tail) > 120 {
					tail = tail[len(tail)-120:]
				}
				return fmt.Sprintf("step %q: %s", step.Name, tail)
			}
			return fmt.Sprintf("step %q failed (exit %d)", step.Name, step.ExitCode)
		}
	}
	if result.Error != "" {
		return result.Error
	}
	return string(result.Conclusion)
}
