# Server Codex Instructions

## Parallel delegation

Use collaboration subagents proactively when a task has at least two independent
investigation tracks and parallel work can reduce end-to-end time. In particular,
start multiple subagents for MR reviews with multiple independent change-impact paths and production incident
investigations after the root agent has established the exact scope and paths.

- Keep at most five subagents active at once on this server.
- Give each subagent a distinct, bounded ownership area and the verified absolute
  paths or existing evidence it needs. Avoid duplicate repository scans.
- Keep subagents read-only when they share a checkout. The root agent owns code
  edits, destructive cleanup, commits, pushes, merge requests, deployments, and
  other external writes unless a subagent has a separate isolated worktree and
  explicit ownership.
- Give every top-level task that may modify source a unique Git worktree and
  branch. Never reuse or switch another active task's checkout.
- Do not have multiple agents run the same build or broad test suite. The root
  agent selects one proportionate validation pass after gathering results.
- Wait for all required subagents, reconcile conflicting conclusions, and verify
  important findings against source evidence before reporting or editing.
- Do not delegate trivial work, strictly sequential steps, or tasks whose
  coordination overhead is likely to exceed the saved time.
