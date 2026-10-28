import { test, expect } from "./fixtures.js";

const mockDiff = `diff --git a/pkg/agent/antigravity/executor.go b/pkg/agent/antigravity/executor.go
index 8a3f912..4c91b2e 100644
--- a/pkg/agent/antigravity/executor.go
+++ b/pkg/agent/antigravity/executor.go
@@ -102,15 +102,22 @@ func (e *Executor) ExecuteTask(ctx context.Context, task *model.Task, config *Config) (*ExecutionResult, error) {
 	if task == nil {
 		return nil, errors.New("task cannot be nil")
 	}
-	log.Printf("Starting execution of task %s with timeout %v", task.ID, task.Timeout)
-	runner := NewTaskRunner(e.client, task.Capabilities, task.WorkingDir)
-	result, err := runner.Run(ctx)
+	log.Printf("Starting execution of task %s with timeout %v (priority: %d, retryPolicy: %s)", task.ID, task.Timeout, task.Priority, task.RetryPolicy)
+	runner := NewTaskRunner(e.client, task.Capabilities, task.WorkingDir, RunnerOptions{
+		MaxRetries:       config.MaxRetries,
+		BackoffDuration:  config.BackoffDuration,
+		EnableLiveStream: true,
+	})
+	result, err := runner.RunWithContext(ctx, config.ExecutionFlags)
 	if err != nil {
-		return nil, fmt.Errorf("task runner failed: %w", err)
+		log.Printf("Task runner failed for %s: %v", task.ID, err)
+		return nil, fmt.Errorf("task runner failed for task %s (attempts=%d): %w", task.ID, result.Attempts, err)
 	}
+	e.metrics.RecordTaskSuccess(task.ID, result.Duration)
 	return result, nil
 }
diff --git a/pkg/ui/server.go b/pkg/ui/server.go
index 1234567..89abcde 100644
--- a/pkg/ui/server.go
+++ b/pkg/ui/server.go
@@ -45,6 +45,10 @@ func (s *Server) Routes() http.Handler {
 	mux.HandleFunc("/api/tasks", s.handleTasks)
 	mux.HandleFunc("/api/tasks/", s.handleTaskDetail)
 	mux.HandleFunc("/api/repos", s.handleRepos)
+	mux.HandleFunc("/api/repos/{owner}/{name}/compare", s.handleRepoCompare)
+	mux.HandleFunc("/api/repos/{owner}/{name}/pulls", s.handleRepoPulls)
+	mux.HandleFunc("/api/repos/{owner}/{name}/releases", s.handleRepoReleases)
+	mux.HandleFunc("/api/metrics", s.handleMetrics)
 	return mux
 }
diff --git a/ui/src/components/DiffView.jsx b/ui/src/components/DiffView.jsx
new file mode 100644
index 0000000..fe45210
--- /dev/null
+++ b/ui/src/components/DiffView.jsx
@@ -0,0 +1,15 @@
+import React from "react";
+
+export default function DiffView({ diff }) {
+  return <div className="diff-view">{diff}</div>;
+}
`;

test.describe("internal PR diff viewing and switching", () => {
  test("renders side-by-side, inline, and raw diffs properly on internal PRs", async ({
    page,
  }) => {
    // Stubbed on purpose: GET /api/repos/{owner}/{name}/compare is grain
    // asking GitHub's compare API for the diff, and this demo has no
    // GitHub and no repository behind it. The stub is a fixed multi-file
    // diff for the layout switch; internal-pr-diff.spec.js draws a real
    // one, from a branch pushed into `grain demo -github-sim`'s repo.
    await page.route("**/api/repos/bwsalmon/grain/compare*", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          base: "main",
          head: "grain/task-380",
          mergeBase: "a1b2c3d4e5f6",
          merged: false,
          diff: mockDiff,
        }),
      });
    });

    await page.goto("/repos/bwsalmon/grain/compare/main...grain/task-380");
    await expect(
      page.getByRole("heading", { name: "grain/task-380" }),
    ).toBeVisible();

    const changes = page.getByRole("region", { name: "Branch changes" });
    await expect(changes).toBeVisible();

    // Default layout is side by side
    const sideBySideBtn = page.getByRole("button", { name: "Side by side" });
    const inlineBtn = page.getByRole("button", { name: "Inline" });
    const rawBtn = page.getByRole("button", { name: "Raw" });

    await expect(sideBySideBtn).toHaveAttribute("aria-pressed", "true");
    await expect(changes.locator("table.diff-split")).toHaveCount(3);
    await expect(changes).toContainText("Starting execution of task");
    await page.screenshot({
      path: "test-results/diff-screen-side-by-side.png",
      fullPage: true,
    });

    // Switch to Inline diff
    await inlineBtn.click();
    await expect(inlineBtn).toHaveAttribute("aria-pressed", "true");
    await expect(changes.locator("table.diff-split")).toHaveCount(0);
    await expect(changes.locator("table.diff-unified")).toHaveCount(3);
    await expect(changes).toContainText("RunWithContext");
    await page.screenshot({
      path: "test-results/diff-screen-inline.png",
      fullPage: true,
    });

    // Switch to Raw diff
    await rawBtn.click();
    await expect(rawBtn).toHaveAttribute("aria-pressed", "true");
    await expect(changes.locator("table")).toHaveCount(0);
    await expect(changes.locator("pre")).toBeVisible();
    await expect(changes).toContainText(
      "diff --git a/pkg/agent/antigravity/executor.go",
    );
    await page.screenshot({
      path: "test-results/diff-screen-raw.png",
      fullPage: true,
    });

    // Switch back to Side by side
    await sideBySideBtn.click();
    await expect(sideBySideBtn).toHaveAttribute("aria-pressed", "true");
    await expect(changes.locator("table.diff-split")).toHaveCount(3);
  });
});
