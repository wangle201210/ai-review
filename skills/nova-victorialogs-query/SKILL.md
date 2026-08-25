---
name: nova-victorialogs-query
description: Apply Nova game-platform defaults to the official VictoriaLogs query workflow. Use when Codex must search or analyze nova-game-play logs in the sg, us, or 2j environment by user ID, game, round or draw ID, trace ID, error text, container name, or time range; reconstruct an incident timeline; or correlate logs with source code only when explicitly requested. Default unspecified queries to the 2j environment.
---

# Nova VictoriaLogs Query

Use this companion skill for Nova-specific routing, filters, defaults, and incident analysis. Use the official `victorialogs-query` skill for LogsQL syntax, VictoriaLogs endpoints, and response formats.

## Load the official skill

1. Read the available `$victorialogs-query` skill's `SKILL.md` completely before querying. Resolve its path from the current skill catalog; a standard installation from this repository places it at `~/.codex/skills/victorialogs-query/SKILL.md`.
2. Read its `references/api-reference.md` only when endpoint parameters or response details are needed.
3. Apply the Nova constraints in this skill in addition to the official workflow. Do not modify the installed companion skill while querying logs.

## Resolve the environment

Use these base URLs. Kubernetes namespaces are routing-dependent and must not be treated as a fixed environment-wide constraint:

| Environment | Query base URL |
| --- | --- |
| `sg` | `https://v-log.local-sg.net` |
| `us` | `https://v-log.local-us.net` |
| `2j` | `https://v-log.local-2j.net` |

- Use `2j` when the user does not specify an environment or cluster.
- Honor an explicit `sg`, `us`, or `2j` environment or cluster.
- Infer the cluster from a supplied `v-log.local-{cluster}.net` VMUI URL.
- Set the resolved URL for the current query only. Do not edit shell startup files.
- Do not probe an environment merely to validate the mapping unless the user requests validation.

## Apply Nova defaults

- Namespace filters are optional. Do not impose a fixed namespace merely from the environment name.
- When a game is routed through a swimlane, discover the actual namespace from routing or global logs before adding a namespace filter.
- When the user explicitly names a game or container, add the exact container constraint when useful:

```bash
--data-urlencode 'extra_stream_filters={"kubernetes.container_name":"firejokern"}'
```

- Add `kubernetes.namespace_name` to `extra_stream_filters` only when it is explicitly supplied or discovered from routing evidence.
- Do not add a container constraint when no game or container is named.
- Treat times without an explicit timezone as `Asia/Shanghai` and convert API parameters to RFC3339.
- Display incident timelines in `Asia/Shanghai` unless the user requests another timezone.
- Query the latest `15m` when no time range is supplied.
- Use explicit `start` and `end` parameters for every log query. Never run an unbounded query.
- Limit ordinary log searches to `500` records unless the user asks for another bounded limit.

## Query workflow

1. Extract the environment or cluster, game or container, exact identifiers, error text, and the smallest useful time range.
2. Resolve the environment URL. If a namespace is needed to narrow the search, derive it from explicit input or routing evidence rather than a fixed environment mapping.
3. Build LogsQL according to the official skill. Quote all user-supplied values safely.
4. For an initial incident search containing multiple identifiers, use OR matching so related events containing any identifier are retained. Use AND only when the user explicitly requires all terms in each record.
5. Prefer POST with `--data-urlencode`. Save JSON Lines to a new mode-`0600` temporary file instead of printing queried logs to the terminal.
6. Parse the saved JSON Lines with `jq`, structured tools, or bounded reads. Sort chronologically by `_time` for a timeline while preserving millisecond precision.
7. Keep the result file through the requested analysis. Do not repeat the same query just because results were not printed to stdout.

Use this command shape, substituting explicit RFC3339 timestamps and a safely quoted LogsQL query:

```bash
nova_vm_logs_url="https://v-log.local-2j.net"
nova_result_file=$(mktemp "${TMPDIR:-/tmp}/nova-victorialogs.XXXXXX")
chmod 600 "$nova_result_file"
curl -q --config "${VM_CURL_CONFIG:-/dev/null}" -sS --fail-with-body \
  --data-urlencode 'query=<LogsQL query>' \
  "$nova_vm_logs_url/select/logsql/query?start=<RFC3339>&end=<RFC3339>&limit=500" \
  > "$nova_result_file"
```

## Analyze incidents

1. Identify the first abnormal state or error, not merely the final disconnect or cleanup event.
2. Separate the direct crash site, any earlier invariant violation, and later recovery or retry failures.
3. Correlate user, game or draw, trace, pod, container, endpoint, and stream identifiers across records.
4. Preserve millisecond ordering. Note that equal timestamps may not imply deterministic ingestion order.
5. State clearly which conclusions are inferred rather than directly proven by logs.

## Inspect source only when requested

Do not read or enumerate source repositories for a log-only request. When the user explicitly asks for code correlation or root-cause analysis from source, use `$nova-game-play-code-analysis` and pass it the existing result file plus the relevant container, service, module, version, and stack-path evidence. Let that skill resolve the project root and revision. Diagnose without editing code unless the user explicitly requests a fix.

## Handle credentials and logs safely

- Treat all operations as read-only.
- Use `VM_CURL_CONFIG` for authentication only when required, and never print or read its contents into the conversation.
- Never put tokens, cookies, copied headers, or production log records in this skill directory.
- Browser headers, VMUI fragment parameters, and display settings are not credentials.
