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

package ci

import (
	"fmt"
	"strings"
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
func IsJobRetryEligible(job Job, result JobRun, attempt int) bool {
	if attempt >= MaxJobRetries {
		return false
	}
	if result.Conclusion == ConclusionSuccess || result.Conclusion == ConclusionCancelled || result.Conclusion == ConclusionSkipped {
		return false
	}
	// Designated retry policy on the job
	if job.Retry || job.Retries > 0 || job.RetryPolicy != "" {
		return true
	}
	// Infrastructure-classified failures
	if result.Conclusion == ConclusionErrored {
		return true
	}
	if IsTransientOrInfrastructureError(result.Error) {
		return true
	}
	for _, step := range result.Steps {
		if step.Conclusion != ConclusionSuccess {
			if IsTransientOrInfrastructureError(step.Error) || IsTransientOrInfrastructureError(step.Output) {
				return true
			}
		}
	}
	// Also check if any step in Job definition is marked as Infrastructure
	for _, step := range job.Steps {
		if step.Infrastructure {
			for _, stepRun := range result.Steps {
				if stepRun.Name == step.DisplayName() && stepRun.Conclusion != ConclusionSuccess {
					return true
				}
			}
		}
	}
	return false
}

// SummarizeFailure produces a short description of the failure for logging and flake warnings.
func SummarizeFailure(result JobRun) string {
	for _, step := range result.Steps {
		if step.Conclusion != ConclusionSuccess && step.Conclusion != ConclusionSkipped {
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
