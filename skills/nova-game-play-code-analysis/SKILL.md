---
name: nova-game-play-code-analysis
description: Inspect and analyze Nova game-play source code under ~/game-play using incident evidence such as log messages, stack traces, service or container names, endpoints, module versions, trace IDs, and game, round, or draw identifiers. Acquire a missing Nova project and prepare a requested branch or tag when source analysis requires it. Use only when the user explicitly asks Codex to inspect a project's source, correlate logs with code, locate a root cause, explain an implementation path, or review or implement a fix. Do not use for log querying alone.
---

# Nova Game Play Code Analysis

## Enforce scope

Use `~/game-play` as the only project root. Treat each project as one direct
child directory, `~/game-play/<project>`. Do not read, search, modify, or clean
another project while preparing the requested project.

Do not query VictoriaLogs with this skill. Work from evidence supplied by the
user or from a result produced by `$nova-victorialogs-query`.

Repository acquisition and revision preparation are permitted when needed for
the requested source analysis. Otherwise prefer read-only inspection. Diagnose
without editing code unless the user explicitly requests a fix. Do not commit,
push, or create a merge request unless the user explicitly requests that
workflow.

Do not run builds, tests, generators, dependency installation, Docker commands,
or services unless the user explicitly requests them and the server's global
resource constraints permit them.

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

1. Create `~/game-play` if necessary, without modifying its existing children.
2. Clone only the canonical origin. Clone into a uniquely named temporary
   sibling under `~/game-play`, not directly into the final path, so a failed
   clone cannot leave a partial target.
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

Repeat these checks immediately before any destructive recovery command. Use
`git -C <exact-target>` for every Git operation; do not rely on the process
working directory.

## Prepare the requested revision

If the user supplies a branch or tag, validate it as a Git ref before using it.
Quote it in every command and do not interpret it as shell syntax.

1. Inspect local heads, local tags, and the exact matching remote head or tag.
   Fetch only the requested remote ref when it exists; do not run
   `git fetch --all`.
2. Prefer a branch over a same-named tag. Switch according to what exists:
   - Existing local branch: `git switch <branch>`.
   - Remote branch only: create a local tracking branch from
     `origin/<branch>`.
   - Tag only: switch to the tag in detached-HEAD mode.
3. If the switch succeeds, preserve any nonblocking working-tree changes and
   disclose them before analyzing the code.
4. If and only if Git reports that local changes would be overwritten and the
   switch is blocked, apply the recovery procedure below.
5. After a successful switch, use `git pull --ff-only` only for a branch that
   has an upstream. If it cannot fast-forward, stop and preserve local commits;
   never force-reset the branch to its remote.

When no revision is supplied, inspect the current revision and report its
branch or detached commit. Do not choose or switch branches speculatively.

## Recover from a blocked switch

The user authorizes discarding local working-tree changes only when those
changes prevent switching to the requested revision. This permission does not
authorize deleting commits, branches, ignored files, other repositories, or
the project root.

1. Re-verify the exact target and `origin`.
2. Capture and report `git status --short` so the discarded paths are known.
3. Run `git reset --hard HEAD` in the exact verified repository to discard
   tracked working-tree and index changes, then retry the switch.
4. If the retry is still blocked specifically by colliding untracked files,
   preview `git clean -nd`, report the paths, and then run `git clean -fd` in
   that exact repository. Never use `-x` or `-X`; ignored files must remain.
5. Retry the switch once more. If it still fails, stop and report the exact
   error rather than escalating to broader cleanup.

Never delete local branches, discard unpushed commits, run
`git reset --hard origin/<branch>`, or run recursive filesystem deletion as
part of branch preparation.

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
