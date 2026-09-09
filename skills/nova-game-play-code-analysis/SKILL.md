---
name: nova-game-play-code-analysis
description: Inspect and analyze Nova game-play source code under ~/game-play using incident evidence such as log messages, stack traces, service or container names, endpoints, module versions, trace IDs, and game, round, or draw identifiers. Acquire a missing Nova project and prepare a requested branch or tag when source analysis requires it. Use only when the user explicitly asks Codex to inspect a project's source, correlate logs with code, locate a root cause, explain an implementation path, or review or implement a fix. Do not use for log querying alone.
---

# Nova Game Play Code Analysis

## Enforce scope

Use `~/game-play` as the only project root. Treat each project as one direct
canonical repository, `~/game-play/<project>`, and put task worktrees under
`~/game-play/.worktrees`. Do not read, search, modify, or clean another project
while preparing the requested project.

Do not query VictoriaLogs with this skill. Work from evidence supplied by the
user or from a result produced by `$nova-victorialogs-query`.

Repository acquisition and revision preparation are permitted when needed for
the requested source analysis. Otherwise prefer read-only inspection. Diagnose
without editing code unless the user explicitly requests a fix. Do not commit,
push, or create a merge request unless the user explicitly requests that
workflow.

## Resolve the project

1. Determine the project from the user's explicit project name or strong
   incident evidence such as the service, container, or Go import path. If
   multiple projects remain plausible, ask the user instead of preparing a
   guessed repository.
2. Validate the project name before interpolating it into a path, URL, or shell
   command. Accept only names matching `^[A-Za-z0-9][A-Za-z0-9._-]*$`; reject
   `.`, `..`, slashes, whitespace, shell metacharacters, and path traversal.
3. Set the exact target to `~/game-play/<project>` and the canonical origin to
   `ssh://git@git.easycodesource.com:2222/nova/game-play/<project>.git`.
4. Resolve and quote all paths. Reject a target that is a symbolic link or is
   not an exact direct child of the resolved `~/game-play` directory.

## Acquire a missing project

When the exact target does not exist:

1. Create `~/game-play` and `~/game-play/.locks` if necessary. Acquire the
   project-specific `flock`, then recheck whether the target now exists so two
   concurrent tasks cannot install the same canonical repository.
2. Clone only the canonical origin while holding that short lock. Clone into a
   uniquely named temporary sibling under `~/game-play`, not directly into the
   final path, so a failed clone cannot leave a partial target.
3. Verify the temporary clone is a Git worktree whose top level is that exact
   temporary directory and whose `origin` identifies
   `git.easycodesource.com:2222/nova/game-play/<project>.git`.
4. Atomically rename the verified temporary clone to the exact target. Stop if
   another process created the target first.
5. Remove only the temporary directory created by this attempt when the clone
   or verification fails. Never clean or delete another child of
   `~/game-play`.

Do not search unrelated directories when a clone fails. Report the SSH,
permission, network, or repository-not-found error with secrets redacted.

## Verify an existing project

Before fetching, switching, resetting, or cleaning an existing target:

1. Confirm it is a Git worktree and `git rev-parse --show-toplevel` resolves to
   the exact target directory.
2. Read `git remote get-url origin` and verify that it identifies the same host,
   SSH port, namespace, and project as the canonical origin. Allow an
   equivalent Git SSH spelling, but reject a different host, namespace, or
   project.
3. Stop on either verification failure. Never run destructive Git commands in
   an unverified directory.

Repeat these checks immediately before fetching or changing worktree metadata.
Use `git -C <exact-target>` for every Git operation; do not rely on the process
working directory.

## Prepare the requested revision in a worktree

If the user supplies a branch or tag, validate it as a Git ref before using it.
Quote it in every command and do not interpret it as shell syntax.

1. Never switch, reset, or clean the canonical repository for a task. It is a
   shared base that can be used by multiple concurrent Codex requests.
2. Create `~/game-play/.locks` and use `flock` on the project-specific lock file
   only while cloning, fetching the exact required ref, or adding/removing a
   worktree. Run the protected operation within the same `flock` command because
   shell state and file descriptors do not persist across tool calls. Do not
   hold the lock during analysis, edits, or tests.
3. Fetch only the exact requested remote ref when needed; do not run
   `git fetch --all`. Resolve the requested branch, tag, or commit while holding
   the short repository lock.
4. Create a uniquely named task directory directly under
   `~/game-play/.worktrees`, including the project and task purpose. Reject a
   path that already exists or resolves outside this directory.
5. For read-only analysis, add a detached worktree at the exact commit. For an
   authorized fix, create a uniquely named branch and worktree from the intended
   base, normally `origin/main` for incident remediation. Never reuse another
   task's worktree or branch.
6. Resolve and retain the absolute task-worktree root. Bind every Git and source
   command to it with `git -C`, absolute paths, or an explicit same-command
   `cd`; shell working directories do not persist between tool calls.
7. Before cleanup, verify the exact task root, repository, branch, and status.
   Remove only this task's clean worktree while holding the project lock. Leave
   a worktree in place and report it if it has uncommitted or unpushed work.

When no revision is supplied, resolve the intended base from the request or the
verified canonical repository, report it, and still use a unique task worktree.
Never delete local branches, discard unpushed commits, or rewrite shared
history as part of preparation.

## Analyze the code

1. Extract the service, container, module, function, endpoint, identifiers,
   error text, and version evidence relevant to the incident.
2. Search with `rg` and use bounded file reads to reconstruct the execution
   path from input validation through the first abnormal state and final
   failure.
3. When a Go stack contains a module version such as `@v1.51.3`, inspect that
   exact tag first with `git show <tag>:<path>`, then compare it with the current
   checkout when useful.
4. Separate the direct crash site, the earlier invariant violation that allowed
   bad state through, and later recovery or retry failures.
5. Cite exact local files and lines. State clearly which conclusions are
   directly proven by code or evidence and which are inferred.

## Consume log evidence

When `$nova-victorialogs-query` produced a result file, read that existing file
instead of rerunning the log query. Preserve millisecond ordering and correlate
user, game, round, draw, trace, pod, container, and endpoint identifiers across
the relevant code paths.

Never copy production logs, cookies, bearer tokens, or browser headers into
this skill directory.

## Report findings

Lead with the root cause or strongest supported hypothesis. Then provide the
supporting evidence, inspected project and revision, affected code path and
version, uncertainty or missing evidence, and the smallest appropriate fix
direction. Disclose any repository clone, branch switch, discarded paths, or
cleanup performed. Do not implement the fix unless the user explicitly
requests it.
