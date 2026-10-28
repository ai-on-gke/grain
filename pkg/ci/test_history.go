package ci

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// TestExecutionStatus represents the outcome of a test in a specific run.
type TestExecutionStatus string

const (
	TestStatusPass    TestExecutionStatus = "PASS"
	TestStatusFail    TestExecutionStatus = "FAIL"
	TestStatusFlake   TestExecutionStatus = "FLAKE"
	TestStatusSkipped TestExecutionStatus = "SKIPPED"
	// TestStatusQuarantined is a failure of a test the repo's quarantine
	// file excuses: the test failed, but its job passed because of it.
	// Counted apart from FAIL so a quarantined test's history still says
	// how often it fails without that reading as its jobs failing.
	TestStatusQuarantined TestExecutionStatus = "QUARANTINED"
)

// TestExecution records a single execution or occurrence of a test within a CI run/job.
type TestExecution struct {
	RunID     string              `json:"runId"`
	Repo      string              `json:"repo"`
	Ref       string              `json:"ref"`
	Commit    string              `json:"commit,omitempty"`
	JobID     string              `json:"jobId"`
	JobName   string              `json:"jobName,omitempty"`
	Status    TestExecutionStatus `json:"status"`
	StartedAt *time.Time          `json:"startedAt,omitempty"`
	Duration  time.Duration       `json:"duration,omitempty"`
	Message   string              `json:"message,omitempty"`
	Location  string              `json:"location,omitempty"`
}

// TestHistoryReport summarizes the historical performance of a named test.
type TestHistoryReport struct {
	TestName     string  `json:"testName"`
	Repo         string  `json:"repo,omitempty"`
	RunsExamined int     `json:"runsExamined"`
	Executions   int     `json:"executions"`
	Passed       int     `json:"passed"`
	Failed       int     `json:"failed"`
	Flaked       int     `json:"flaked"`
	Skipped      int     `json:"skipped"`
	Quarantined  int     `json:"quarantined"`
	PassRate     float64 `json:"passRate"`
	FailRate     float64 `json:"failRate"`
	FlakeRate    float64 `json:"flakeRate"`
	// QuarantinedRate is the share of decisive executions that failed
	// while quarantined. It is part of the denominator: a quarantined
	// test that fails every run reads as failing every run, not as
	// having no history.
	QuarantinedRate float64         `json:"quarantinedRate"`
	RecentRuns      []TestExecution `json:"recentRuns,omitempty"`
}

var (
	goPassRegex = regexp.MustCompile(`(?m)^\s*--- PASS:\s+([^\s\(\)]+)(?:\s+\(([^)]+)\))?`)
	goSkipRegex = regexp.MustCompile(`(?m)^\s*--- SKIP:\s+([^\s\(\)]+)(?:\s+\(([^)]+)\))?`)
)

// QueryTestHistory inspects runs to extract execution records and pass/fail/flake rates for testName.
func QueryTestHistory(runs []Run, testName string) TestHistoryReport {
	testName = strings.TrimSpace(testName)
	report := TestHistoryReport{
		TestName:     testName,
		RunsExamined: len(runs),
	}
	if len(runs) > 0 && runs[0].Repo != "" {
		report.Repo = runs[0].Repo
	}

	var executions []TestExecution

	for _, run := range runs {
		for _, job := range run.Jobs {
			execs := extractJobTestExecutions(run, job, testName)
			executions = append(executions, execs...)
		}
	}

	// Sort recent executions newest first by StartedAt or Run ID
	sort.SliceStable(executions, func(i, j int) bool {
		ti := timeValue(executions[i].StartedAt)
		tj := timeValue(executions[j].StartedAt)
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return executions[i].RunID > executions[j].RunID
	})

	report.Executions = len(executions)
	report.RecentRuns = executions

	for _, e := range executions {
		switch e.Status {
		case TestStatusPass:
			report.Passed++
		case TestStatusFail:
			report.Failed++
		case TestStatusFlake:
			report.Flaked++
		case TestStatusSkipped:
			report.Skipped++
		case TestStatusQuarantined:
			report.Quarantined++
		}
	}

	totalDecisive := report.Passed + report.Failed + report.Flaked + report.Quarantined
	if totalDecisive > 0 {
		report.PassRate = float64(report.Passed) / float64(totalDecisive) * 100.0
		report.FailRate = float64(report.Failed) / float64(totalDecisive) * 100.0
		report.FlakeRate = float64(report.Flaked) / float64(totalDecisive) * 100.0
		report.QuarantinedRate = float64(report.Quarantined) / float64(totalDecisive) * 100.0
	}

	return report
}

func extractJobTestExecutions(run Run, job JobRun, targetTest string) []TestExecution {
	var results []TestExecution

	// 1. Check Job FailureSummary
	if job.FailureSummary != nil && len(job.FailureSummary.Tests) > 0 {
		for _, tf := range job.FailureSummary.Tests {
			if matchesTestName(tf.Name, targetTest) {
				status := TestStatusFail
				switch {
				case tf.Quarantined:
					status = TestStatusQuarantined
				case job.Flake:
					status = TestStatusFlake
				}
				results = append(results, TestExecution{
					RunID:     run.ID,
					Repo:      run.Repo,
					Ref:       run.Ref,
					Commit:    run.Commit,
					JobID:     job.JobID,
					JobName:   job.Name,
					Status:    status,
					StartedAt: job.StartedAt,
					Duration:  job.Duration(time.Now()),
					Message:   tf.Message,
					Location:  tf.Location,
				})
			}
		}
	}

	// 2. If no FailureSummary hit, check step outputs if available
	if len(results) == 0 {
		hasStepOutput := false
		for _, step := range job.Steps {
			if step.Output != "" {
				hasStepOutput = true
				clean := stripANSI(step.Output)
				// Check for FAIL lines
				failMatches := goTestFailRegex.FindAllStringSubmatch(clean, -1)
				for _, m := range failMatches {
					if len(m) > 1 && matchesTestName(m[1], targetTest) {
						status := TestStatusFail
						switch {
						case quarantinedIn(job, m[1]):
							status = TestStatusQuarantined
						case job.Flake:
							status = TestStatusFlake
						}
						results = append(results, TestExecution{
							RunID:     run.ID,
							Repo:      run.Repo,
							Ref:       run.Ref,
							Commit:    run.Commit,
							JobID:     job.JobID,
							JobName:   job.Name,
							Status:    status,
							StartedAt: job.StartedAt,
							Duration:  job.Duration(time.Now()),
						})
					}
				}
				// Check for PASS lines
				passMatches := goPassRegex.FindAllStringSubmatch(clean, -1)
				for _, m := range passMatches {
					if len(m) > 1 && matchesTestName(m[1], targetTest) {
						results = append(results, TestExecution{
							RunID:     run.ID,
							Repo:      run.Repo,
							Ref:       run.Ref,
							Commit:    run.Commit,
							JobID:     job.JobID,
							JobName:   job.Name,
							Status:    TestStatusPass,
							StartedAt: job.StartedAt,
							Duration:  job.Duration(time.Now()),
						})
					}
				}
				// Check for SKIP lines
				skipMatches := goSkipRegex.FindAllStringSubmatch(clean, -1)
				for _, m := range skipMatches {
					if len(m) > 1 && matchesTestName(m[1], targetTest) {
						results = append(results, TestExecution{
							RunID:     run.ID,
							Repo:      run.Repo,
							Ref:       run.Ref,
							Commit:    run.Commit,
							JobID:     job.JobID,
							JobName:   job.Name,
							Status:    TestStatusSkipped,
							StartedAt: job.StartedAt,
							Duration:  job.Duration(time.Now()),
						})
					}
				}
			}
		}

		// 3. Fallback: If step outputs were not retained (e.g. summarized run list)
		// and the job succeeded, and is a test job or general workflow
		if !hasStepOutput && len(results) == 0 && job.Conclusion == ConclusionSuccess {
			if isTestJob(job) {
				results = append(results, TestExecution{
					RunID:     run.ID,
					Repo:      run.Repo,
					Ref:       run.Ref,
					Commit:    run.Commit,
					JobID:     job.JobID,
					JobName:   job.Name,
					Status:    TestStatusPass,
					StartedAt: job.StartedAt,
					Duration:  job.Duration(time.Now()),
				})
			}
		}
	}

	return results
}

// quarantinedIn reports whether job passed with name among the failures
// its quarantine excused -- what a run recorded before its quarantined
// failures were kept in the FailureSummary still says so here.
func quarantinedIn(job JobRun, name string) bool {
	if !job.Quarantined {
		return false
	}
	for _, q := range job.QuarantinedTests {
		if q == name {
			return true
		}
	}
	return false
}

func matchesTestName(foundName, targetName string) bool {
	found := strings.TrimSpace(foundName)
	target := strings.TrimSpace(targetName)
	if found == target {
		return true
	}
	if strings.HasPrefix(found, target+"/") || strings.HasPrefix(found, target+".") {
		return true
	}
	if strings.HasSuffix(found, "/"+target) || strings.HasSuffix(found, "."+target) {
		return true
	}
	return false
}

func isTestJob(job JobRun) bool {
	name := strings.ToLower(job.Name)
	id := strings.ToLower(job.JobID)
	keywords := []string{"test", "go", "unit", "spec", "e2e", "integration", "playwright", "jest"}
	for _, kw := range keywords {
		if strings.Contains(name, kw) || strings.Contains(id, kw) {
			return true
		}
	}
	return false
}

// Render formats the report as a clean Markdown/text block.
func (r TestHistoryReport) Render() string {
	var b strings.Builder
	repoClause := ""
	if r.Repo != "" {
		repoClause = fmt.Sprintf(" (repo: %s)", r.Repo)
	}
	fmt.Fprintf(&b, "Test History: %s%s\n", r.TestName, repoClause)
	fmt.Fprintf(&b, "Runs examined: %d | Executions found: %d\n", r.RunsExamined, r.Executions)

	if r.Executions == 0 {
		b.WriteString("\nNo recorded executions found for this test in the requested runs.")
		return b.String()
	}

	fmt.Fprintf(&b, "\nSummary Statistics:\n")
	fmt.Fprintf(&b, "  • Passed:  %d (%.1f%%)\n", r.Passed, r.PassRate)
	fmt.Fprintf(&b, "  • Failed:  %d (%.1f%%)\n", r.Failed, r.FailRate)
	fmt.Fprintf(&b, "  • Flaked:  %d (%.1f%%)\n", r.Flaked, r.FlakeRate)
	if r.Quarantined > 0 {
		fmt.Fprintf(&b, "  • Quarantined: %d (%.1f%%) -- failed, but quarantined, so the job passed\n", r.Quarantined, r.QuarantinedRate)
	}
	if r.Skipped > 0 {
		fmt.Fprintf(&b, "  • Skipped: %d\n", r.Skipped)
	}

	if len(r.RecentRuns) > 0 {
		b.WriteString("\nRecent Executions:\n")
		limit := len(r.RecentRuns)
		if limit > 15 {
			limit = 15
		}
		for i := 0; i < limit; i++ {
			exec := r.RecentRuns[i]
			timeStr := "unknown time"
			if exec.StartedAt != nil {
				timeStr = exec.StartedAt.UTC().Format("2006-01-02 15:04 UTC")
			}
			commitShort := exec.Commit
			if len(commitShort) > 7 {
				commitShort = commitShort[:7]
			}
			refInfo := exec.Ref
			if commitShort != "" {
				refInfo = fmt.Sprintf("%s @ %s", exec.Ref, commitShort)
			}
			jobName := exec.JobName
			if jobName == "" {
				jobName = exec.JobID
			}
			fmt.Fprintf(&b, "  • %s | run %s (%s) | job %q: %s",
				timeStr, exec.RunID, refInfo, jobName, exec.Status)

			if exec.Location != "" || exec.Message != "" {
				var details []string
				if exec.Location != "" {
					details = append(details, exec.Location)
				}
				if exec.Message != "" {
					firstLine := strings.Split(strings.TrimSpace(exec.Message), "\n")[0]
					details = append(details, firstLine)
				}
				fmt.Fprintf(&b, " (%s)", strings.Join(details, ": "))
			}
			b.WriteString("\n")
		}
	}

	return strings.TrimRight(b.String(), "\n")
}
