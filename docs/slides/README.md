# Grain: Autonomous Engineering Agent Cluster

This slide deck outlines the core architecture, operational workflows, and features of **Grain** — a single-operator agent cluster designed to autonomously convert a backlog of software engineering tasks into tested, verified pull requests.

A dedicated demo cluster was initialized with configured model backends (Gemini 2.5 Pro), scoped Git credentials, sandboxed microVMs, continuous integration workflows, schedules, suites, and interactive Director sessions to produce high-resolution presentation snapshots.

---

## Slide Deck Overview

| Slide | Title | Key Theme | Screenshot |
| :--- | :--- | :--- | :--- |
| **01** | [Create a Task](#slide-1-create-a-task) | Intake, Metadata & Capability Assignment | `01-create-a-task.png` |
| **02** | [Task Picked Up by Agent](#slide-2-task-picked-up-by-agent-limited-concurrency) | Bounded Concurrency & Dynamic Queue Dispatch | `02-task-picked-up-by-agent.png` |
| **03** | [Sandboxes & Ephemeral Credentials](#slide-3-sandboxes--ephemeral-credentials) | MicroVM Isolation & Hardware Telemetry | `03-agent-sandbox-credentials.png` |
| **04** | [Agent Does the Task](#slide-4-agent-does-the-task-live-execution) | Streaming Transcripts & Human-in-the-Loop | `04-agent-executes-task.png` |
| **05** | [CI / Merge Pipeline & Auto-Repair](#slide-5-ci--merge-pipeline-with-autonomous-repair) | Native CI Engine & PR Conflict Auto-Repair | `05-ci-merge-pipeline.png` |
| **06** | [Agent Sub-Tasks & Follow-Ups](#slide-6-agent-sub-tasks--follow-up-workflows) | Autonomous Decomposition & Dependency DAGs | `06-agent-subtasks-followups.png` |
| **07** | [Director Interactive Planning](#slide-7-director-interactive-planning-session) | Conversational Milestone & Backlog Synthesis | `07-director-planning-session.png` |
| **08** | [Scheduled Agents & Test Suites](#slide-8-scheduled-agents--test-suites) | Recurring Regressions & Qualification Suites | `08-schedules-test-suites.png` |
| **09** | [GitOps Cluster Configuration](#slide-9-gitops-cluster-configuration-via-repo-check-ins) | Declarative State, Models & Self-Upgrades | `09-cluster-configuration-gitops.png` |
| **10** | [Grain Inside Google (Capsules & Cloud Run Proxy)](#slide-10-grain-inside-google-capsules--cloud-run-proxy) | Outbound Capsule Tunnels & Zero-Trust IAP Ingress | `10-grain-google-architecture.png` |

---

## Reproducing Screenshots & Running the Demo

To start the demo cluster and re-capture all presentation screenshots:

```bash
# Build the grain binary with embedded UI
make build

# Run the automated Playwright screenshot generator
node docs/slides/generate-screenshots.mjs
```

Or view the interactive slide deck directly in your browser:

```bash
open docs/slides/index.html
```
