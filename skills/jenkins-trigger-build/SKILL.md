---
name: jenkins-trigger-build
description: Preview and trigger authenticated Jenkins builds for Nova game-play projects through the Jenkins HTTP API. Use when a user asks to build, deploy, rebuild, restart, reconfigure, or release one or more Nova projects or a named project group at an exact Git branch, or asks to inspect the Jenkins request before submitting it. Do not use for editing Jenkinsfiles, configuring Jenkins controllers or agents, checking build completion, or working with unrelated CI providers.
---

# Trigger Jenkins Builds

Use the bundled `scripts/trigger_build.py`. Resolve it relative to this
`SKILL.md`; do not assume the current working directory is the skill directory.
The script defaults to a network-free preview and emits machine-readable JSON.

## Collect Inputs

Require a project name, project names, or a bundled project group before
execution. Accept comma-separated values, repeated `--project` flags, or
repeated `--group` flags.

Use `main` as the Git branch, `香港 int2 测试环境 local01` as the deployment
environment, and `FullDeploy` as the operation type when the user does not
provide them. Pass `--branch`, `--deploy-env`, or `--operation-type` only to
override those defaults. Accept `f`, `u`, and `r` as operation type aliases.

When the user names an `all*` group, pass it with `--group <name>`. The script
loads the corresponding file from `references/project-groups/`; never submit a
Jenkins job literally named `all`. Run `--list-groups` to discover bundled
group names.

Read credentials only from `JENKINS_USER` and `JENKINS_TOKEN`. They must be in
the environment of the process running Codex; a systemd service does not load
interactive shell startup files. Never ask the user to send credentials in
chat, print them, place the token in command arguments, or copy it into tracked
files. Use `JENKINS_URL` only when the Jenkins base URL differs from the script
default. `JENKINS_DEPLOY_ENV` and `JENKINS_OPERATION_TYPE` may override the two
defaults at the service level.

## Preview

Run without `--execute` first:

```bash
python3 /path/to/jenkins-trigger-build/scripts/trigger_build.py \
  --project wealth,elves \
  --branch version/v2.15.1
```

Inspect the JSON summary and tell the user that no request was sent. Stop after
the preview when the user is exploring, validating parameters, or asking what
would happen.

## Execute

Add `--execute` only when the user explicitly asks to trigger the build. A
direct request such as "build wealth on branch X in environment Y" is explicit
authorization. For ambiguous requests, show the preview and ask before
executing.

```bash
python3 /path/to/jenkins-trigger-build/scripts/trigger_build.py \
  --project wealth,elves \
  --branch version/v2.15.1 \
  --execute
```

For a project group, replace `--project ...` with `--group all` or the requested
group name.

Never retry a failed submission automatically because a timeout can occur after
Jenkins accepted the build. Report every JSON result and let the user decide
whether to retry.

## Report Results

Interpret `status: "submitted"` as Jenkins accepting the request, not as the
build or deployment succeeding. Report the project, operation type, branch,
environment, HTTP status, and returned URL. On errors, report the HTTP or
connection error without exposing credentials. Use another explicitly
authorized workflow if the user asks to monitor build completion.
