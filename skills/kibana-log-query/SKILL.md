---
name: kibana-log-query
description: Query and analyze Nova game-platform logs in the 2j, us, or sg cluster through the Kibana 7.17 internal bsearch API. Use when Codex needs to run or simplify a Kibana Discover curl, search nova-game-play logs by user ID, game/round/draw ID, trace ID, error phrase, or game/container name within a time range, parse plain application/x-ndjson responses, reconstruct an incident timeline, or summarize errors. Use for log work only; do not use this skill to inspect source code.
---

# Kibana Log Query

Use the bundled script for repeatable searches. Resolve
`scripts/query-kibana-logs.mjs` relative to this `SKILL.md`; do not assume the
current working directory is the skill directory.

## Query workflow

1. Extract the cluster, game/container identifier, other exact identifiers, and
   the smallest useful time range from the user request, curl, or pasted logs.
   Support `2j`, `us`, and `sg`. Use `2j` without asking when the user does not
   specify a cluster.
2. When the user explicitly names a game, pass its exact identifier with
   `--game <name>`. This adds a `match_phrase` filter on
   `kubernetes.container_name`. Do not add this filter when no game is named.
3. Pass a non-default cluster with `--cluster us` or `--cluster sg`.
4. Pass each remaining identifier with a separate `--query`; repeated values
   use OR matching, consistent with Kibana Discover. A game filter and text
   query filters must both match.
5. Use explicit `--start` and `--end` for historical incidents, or
   `--last 10m`-style durations for recent incidents. When the user does not
   specify a time range, query the latest 15 minutes without asking.
6. Run the script with its default `timeline` output:

```bash
node /path/to/kibana-log-query/scripts/query-kibana-logs.mjs \
  --game firejokern \
  --query game-draw-id \
  --query user-id
```

The script writes the result to a permission-restricted file and prints only
`output_file=<absolute-path>` to stdout. Read that file for analysis instead of
expecting log records in the terminal. By default, files are created under the
system temporary directory in `kibana-log-query-results/`. Use `--output` when
a specific new file path is useful. Never overwrite an existing output file.

The script defaults to:

- Cluster: `2j`
- Endpoint pattern: `https://logging-kibana.local-{cluster}.net/internal/bsearch`
- Index: `nova-game-play*`
- Kibana header: `kbn-version: 7.17.12`
- Display timezone: `Asia/Shanghai`
- Time range: latest `15m`
- Maximum documents: `500`

Override defaults with CLI options or the environment variables shown by
`--help`.

Do not interpret an initial `isRunning:true` response as zero hits. The script
polls Kibana's asynchronous search ID until each batch item completes.

## Output modes

- Use `--format timeline` for incident analysis. It writes counts and logs in
  chronological order to a `.log` file.
- Add `--descending` when the latest event matters most.
- Add `--include-stacktrace` when stack traces are stored separately from the
  message.
- Use `--format json` for a full, pretty-printed batch response file.
- Use `--format ndjson` for a file with one plain JSON object per batch item.
- Use `--output /path/to/new-file` to choose a destination. Omit it to get a
  unique temporary file with mode `0600`.

After the query, parse or search the returned file path with structured tools,
`rg`, or bounded file reads. Keep the file available for the requested
analysis. Do not rerun the same production query merely because the result was
not printed to the console.

Run `--help` before changing the script or manually reconstructing its request.

## Analyze incidents

1. Identify the first abnormal state or error, not merely the final disconnect
   or cleanup message.
2. Separate the direct crash site, the earlier invariant violation that allowed
   bad state through, and later recovery or retry failures.
3. Correlate user, game/draw, client, pod, container, and endpoint identifiers
   across services.
4. Preserve millisecond ordering, while noting that events sharing a timestamp
   may not have deterministic ingestion order.
5. State clearly when a conclusion is inferred rather than directly proven by
   logs.

## Hand off source analysis

Do not read, search, or enumerate source code while using this skill. A request
to query, parse, or summarize logs alone does not authorize source inspection.

When the user explicitly requests source-code correlation, preserve the
relevant log lines, identifiers, stack traces, and result file, then use
`$nova-game-play-code-analysis` as a separate skill. Let that skill enforce the
source root and code-analysis boundaries.

## Handle credentials safely

Treat searches as read-only. Never store cookies, bearer tokens, copied browser
headers, or returned production logs inside this skill.

The current endpoint may work through internal network access without an
application credential. If credentials are required, provide them transiently
through `KIBANA_AUTHORIZATION` or `KIBANA_COOKIE`. Do not print these variables,
enable shell tracing, or paste their values into commands or reports.

Browser headers such as `origin`, `referer`, `sec-*`, and `user-agent` are not
authentication. `kbn-version` and bsearch `sessionId` are also not credentials.

## Simplify a copied curl

Retain only the endpoint, `content-type`, `kbn-version`, required
authentication, and request body. Remove browser-only headers unless the server
demonstrably requires one.

Use `/internal/bsearch` as the endpoint and save the plain NDJSON response to a
permission-restricted file before inspecting it. Prefer the bundled script,
which handles this automatically. Infer the cluster from
`logging-kibana.local-{cluster}.net` and infer `--game` from a
`kubernetes.container_name` `match_phrase` when the user supplies a copied
curl.
