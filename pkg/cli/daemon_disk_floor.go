package cli

// The agent pool's live sandbox count used to be folded into the limits
// the CI gate reads (limitsWithAgentPool, agentSandboxCounter), so that a
// job the budget refused could be told how much of it the agent pool's
// *standing reservation* was holding and how few of those slots were
// occupied. There is no standing reservation to explain any more: what
// holds the budget is the sandboxes that exist, agent and CI alike, and a
// refusal now reports that live figure directly (model.Ledger,
// grain/task-28). The count and the wrapper that carried it are gone with
// the sentence they were written for.
