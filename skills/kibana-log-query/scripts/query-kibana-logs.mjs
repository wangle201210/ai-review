#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import { randomUUID } from "node:crypto";
import {
  chmodSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";

const supportedClusters = new Set(["2j", "us", "sg"]);
const defaultLookback = "15m";

function clusterFromEndpoint(endpoint) {
  if (!endpoint) {
    return undefined;
  }
  try {
    const hostname = new URL(endpoint).hostname;
    return /^logging-kibana\.local-(2j|us|sg)\.net$/.exec(hostname)?.[1];
  } catch {
    return undefined;
  }
}

function endpointForCluster(cluster) {
  return `https://logging-kibana.local-${cluster}.net/internal/bsearch`;
}

const environmentEndpoint = process.env.KIBANA_BSEARCH_URL;
const defaultCluster =
  process.env.KIBANA_CLUSTER ??
  clusterFromEndpoint(environmentEndpoint) ??
  "2j";

const defaults = {
  cluster: defaultCluster,
  endpoint: environmentEndpoint,
  index: process.env.KIBANA_INDEX ?? "nova-game-play*",
  kbnVersion: process.env.KIBANA_VERSION ?? "7.17.12",
  timeZone: process.env.KIBANA_TIME_ZONE ?? "Asia/Shanghai",
  size: 500,
  format: "timeline",
  order: "asc",
  includeStacktrace: false,
};

const usage = `Query Kibana Discover logs through the internal bsearch endpoint.

Usage:
  query-kibana-logs.mjs [--query <text>] [--game <name>] [time range]

Search filters (at least one required):
  -q, --query <text>       Exact phrase to find. Repeat for OR matching.
  --game <name>            Exact game/container name. Filters
                           kubernetes.container_name with match_phrase.

Time range:
  --start <ISO>            Range start, for example 2026-07-28T01:47:08.525Z.
  --end <ISO>              Range end. Required with --start.
  --last <duration>        Range ending now: 30s, 10m, 2h, or 1d.
                           Defaults to ${defaultLookback} when omitted.

Options:
  --cluster <name>         Kibana cluster: 2j, us, or sg (default: ${defaults.cluster}).
  --endpoint <url>         Override the cluster-derived bsearch URL.
  --index <pattern>        Elasticsearch index pattern (default: ${defaults.index}).
  --kbn-version <version>  Kibana version header (default: ${defaults.kbnVersion}).
  --timezone <zone>        Timeline display timezone (default: ${defaults.timeZone}).
  --size <number>          Maximum returned documents, 1-5000 (default: 500).
  --format <name>          Output file format: timeline, json, or ndjson
                           (default: timeline).
  --output <path>          Write to a new file at this path. By default, create
                           a secure temporary file. Existing files are not
                           overwritten.
  --descending             Write newest documents first.
  --include-stacktrace     Include stacktrace fields after each log message.
  -h, --help               Show this help.

Environment:
  KIBANA_CLUSTER, KIBANA_BSEARCH_URL, KIBANA_INDEX, KIBANA_VERSION
  KIBANA_TIME_ZONE
  KIBANA_AUTHORIZATION     Complete Authorization header value.
  KIBANA_COOKIE            Complete Cookie header value.
  KIBANA_CA_CERT           CA certificate path passed to curl --cacert.

Examples:
  query-kibana-logs.mjs -q user-id

  query-kibana-logs.mjs --cluster us --game firejokern

  query-kibana-logs.mjs \\
    --game firejokern -q game-draw-id -q user-id \\
    --start 2026-01-15T01:47:08.525Z --end 2026-01-15T01:47:19.715Z

  query-kibana-logs.mjs -q user-id --last 10m --include-stacktrace
`;

function fail(message, exitCode = 2) {
  console.error(`error: ${message}`);
  process.exit(exitCode);
}

function requireValue(argv, index, option) {
  const value = argv[index + 1];
  if (value === undefined || value.startsWith("--")) {
    fail(`${option} requires a value`);
  }
  return value;
}

function parseDuration(value) {
  const match = /^(\d+)(s|m|h|d)$/.exec(value);
  if (!match) {
    fail(`invalid duration "${value}"; use forms such as 30s, 10m, 2h, or 1d`);
  }
  const scales = { s: 1000, m: 60_000, h: 3_600_000, d: 86_400_000 };
  return Number(match[1]) * scales[match[2]];
}

function parseArgs(argv) {
  const options = { ...defaults, queries: [] };
  let start;
  let end;
  let last;
  let clusterWasSet = false;
  let endpointWasSet = false;

  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    switch (arg) {
      case "-h":
      case "--help":
        console.log(usage);
        process.exit(0);
        break;
      case "-q":
      case "--query":
        options.queries.push(requireValue(argv, i, arg));
        i += 1;
        break;
      case "--game":
        if (options.game !== undefined) {
          fail("--game may only be provided once");
        }
        options.game = requireValue(argv, i, arg).trim();
        if (!options.game) {
          fail("--game must not be empty");
        }
        i += 1;
        break;
      case "--start":
        start = requireValue(argv, i, arg);
        i += 1;
        break;
      case "--end":
        end = requireValue(argv, i, arg);
        i += 1;
        break;
      case "--last":
        last = requireValue(argv, i, arg);
        i += 1;
        break;
      case "--cluster":
        options.cluster = requireValue(argv, i, arg).toLowerCase();
        clusterWasSet = true;
        i += 1;
        break;
      case "--endpoint":
        options.endpoint = requireValue(argv, i, arg);
        endpointWasSet = true;
        i += 1;
        break;
      case "--index":
        options.index = requireValue(argv, i, arg);
        i += 1;
        break;
      case "--kbn-version":
        options.kbnVersion = requireValue(argv, i, arg);
        i += 1;
        break;
      case "--timezone":
        options.timeZone = requireValue(argv, i, arg);
        i += 1;
        break;
      case "--size":
        options.size = Number(requireValue(argv, i, arg));
        i += 1;
        break;
      case "--format":
        options.format = requireValue(argv, i, arg);
        i += 1;
        break;
      case "--output":
        options.output = requireValue(argv, i, arg);
        i += 1;
        break;
      case "--descending":
        options.order = "desc";
        break;
      case "--include-stacktrace":
        options.includeStacktrace = true;
        break;
      default:
        fail(`unknown option "${arg}"`);
    }
  }

  if (options.queries.length === 0 && !options.game) {
    fail(
      "at least one --query or --game is required; unbounded searches are disabled",
    );
  }
  if (!supportedClusters.has(options.cluster)) {
    fail('--cluster must be "2j", "us", or "sg"');
  }
  if (clusterWasSet && !endpointWasSet) {
    options.endpoint = undefined;
  }
  options.endpoint ??= endpointForCluster(options.cluster);
  options.cluster = clusterFromEndpoint(options.endpoint) ?? options.cluster;
  try {
    new URL(options.endpoint);
  } catch {
    fail("--endpoint must be a valid URL");
  }
  if (!Number.isInteger(options.size) || options.size < 1 || options.size > 5000) {
    fail("--size must be an integer between 1 and 5000");
  }
  if (!["timeline", "json", "ndjson"].includes(options.format)) {
    fail('--format must be "timeline", "json", or "ndjson"');
  }
  if (last !== undefined && (start !== undefined || end !== undefined)) {
    fail("use either --last or --start/--end, not both");
  }
  if (last === undefined && start === undefined && end === undefined) {
    last = defaultLookback;
  }
  if (last !== undefined) {
    const endDate = new Date();
    const startDate = new Date(endDate.getTime() - parseDuration(last));
    options.start = startDate.toISOString();
    options.end = endDate.toISOString();
  } else {
    if (start === undefined || end === undefined) {
      fail("provide both --start and --end, or use --last");
    }
    const startDate = new Date(start);
    const endDate = new Date(end);
    if (Number.isNaN(startDate.getTime()) || Number.isNaN(endDate.getTime())) {
      fail("--start and --end must be valid ISO timestamps");
    }
    if (startDate >= endDate) {
      fail("--start must be earlier than --end");
    }
    options.start = startDate.toISOString();
    options.end = endDate.toISOString();
  }

  try {
    new Intl.DateTimeFormat("sv-SE", { timeZone: options.timeZone }).format();
  } catch {
    fail(`invalid timezone "${options.timeZone}"`);
  }

  return options;
}

function buildFilter(options) {
  const filter = [];
  if (options.queries.length > 0) {
    filter.push({
      bool: {
        should: options.queries.map((query) => ({
          multi_match: {
            type: "phrase",
            query,
            lenient: true,
          },
        })),
        minimum_should_match: 1,
      },
    });
  }
  filter.push({
    range: {
      "@timestamp": {
        format: "strict_date_optional_time",
        gte: options.start,
        lte: options.end,
      },
    },
  });
  if (options.game) {
    filter.push({
      match_phrase: {
        "kubernetes.container_name": options.game,
      },
    });
  }
  return filter;
}

function buildRequest(options) {
  const filter = buildFilter(options);
  const preference = Date.now();
  const commonOptions = {
    sessionId: randomUUID(),
    isRestore: false,
    strategy: "ese",
    isStored: false,
    executionContext: {
      type: "application",
      name: "discover",
      description: "query logs",
      url: "/app/discover",
      id: "",
    },
  };

  return {
    batch: [
      {
        request: {
          params: {
            index: options.index,
            body: {
              sort: [
                {
                  "@timestamp": {
                    order: "desc",
                    unmapped_type: "boolean",
                  },
                },
              ],
              fields: [
                { field: "*", include_unmapped: true },
                { field: "@timestamp", format: "strict_date_optional_time" },
                { field: "timestamp_end", format: "strict_date_optional_time" },
                { field: "ts", format: "strict_date_optional_time" },
              ],
              size: options.size,
              version: true,
              script_fields: {},
              stored_fields: ["*"],
              runtime_mappings: {},
              _source: false,
              query: {
                bool: {
                  must: [],
                  filter,
                  should: [],
                  must_not: [],
                },
              },
            },
            preference,
          },
        },
        options: commonOptions,
      },
      {
        request: {
          params: {
            index: options.index,
            body: {
              size: 0,
              script_fields: {},
              stored_fields: ["*"],
              runtime_mappings: {},
              query: {
                bool: {
                  must: [],
                  filter,
                  should: [],
                  must_not: [],
                },
              },
            },
            track_total_hits: true,
            preference,
          },
        },
        options: {
          ...commonOptions,
          sessionId: randomUUID(),
          executionContext: {
            ...commonOptions.executionContext,
            description: "count logs",
          },
        },
      },
    ],
  };
}

function rejectHeaderNewlines(name, value) {
  if (value !== undefined && /[\r\n]/.test(value)) {
    fail(`${name} must not contain newlines`);
  }
}

function postBsearch(options, requestBody) {
  const endpoint = new URL(options.endpoint);
  endpoint.searchParams.delete("compress");
  const curlArgs = [
    "-sS",
    "--fail-with-body",
    "--connect-timeout",
    "10",
    "--max-time",
    "90",
    endpoint.toString(),
    "-H",
    "accept: */*",
    "-H",
    "content-type: application/json",
    "-H",
    `kbn-version: ${options.kbnVersion}`,
    "-H",
    `origin: ${endpoint.origin}`,
    "-H",
    `referer: ${endpoint.origin}/app/discover`,
    "--data-binary",
    JSON.stringify(requestBody),
  ];

  if (process.env.KIBANA_CA_CERT) {
    curlArgs.push("--cacert", process.env.KIBANA_CA_CERT);
  }

  const authorization = process.env.KIBANA_AUTHORIZATION;
  const cookie = process.env.KIBANA_COOKIE;
  rejectHeaderNewlines("KIBANA_AUTHORIZATION", authorization);
  rejectHeaderNewlines("KIBANA_COOKIE", cookie);

  let secretDirectory;
  try {
    if (authorization || cookie) {
      secretDirectory = mkdtempSync(join(tmpdir(), "kibana-log-query-"));
      const headerFile = join(secretDirectory, "headers");
      const headers = [
        authorization ? `Authorization: ${authorization}` : undefined,
        cookie ? `Cookie: ${cookie}` : undefined,
      ]
        .filter(Boolean)
        .join("\n");
      writeFileSync(headerFile, `${headers}\n`, { mode: 0o600 });
      chmodSync(headerFile, 0o600);
      curlArgs.push("-H", `@${headerFile}`);
    }

    const result = spawnSync("curl", curlArgs, {
      encoding: "utf8",
      maxBuffer: 100 * 1024 * 1024,
    });
    if (result.error) {
      fail(`could not execute curl: ${result.error.message}`, 1);
    }
    if (result.status !== 0) {
      const detail = [result.stderr.trim(), result.stdout.trim()]
        .filter(Boolean)
        .join("\n");
      fail(`Kibana request failed (curl exit ${result.status})${detail ? `\n${detail}` : ""}`, 1);
    }
    return result.stdout;
  } finally {
    if (secretDirectory) {
      rmSync(secretDirectory, { recursive: true, force: true });
    }
  }
}

function decodeLine(line, lineNumber) {
  const trimmed = line.trim();
  if (!trimmed) {
    return undefined;
  }
  try {
    return JSON.parse(trimmed);
  } catch (error) {
    fail(`could not parse response line ${lineNumber}: ${error.message}`, 1);
  }
}

function decodeResponse(response) {
  return response
    .split(/\r?\n/)
    .map((line, index) => decodeLine(line, index + 1))
    .filter((value) => value !== undefined);
}

function sleep(milliseconds) {
  Atomics.wait(
    new Int32Array(new SharedArrayBuffer(Int32Array.BYTES_PER_ELEMENT)),
    0,
    0,
    milliseconds,
  );
}

function executeSearch(options, requestBody) {
  const deadline = Date.now() + 120_000;
  let pending = requestBody.batch.map((item, originalId) => ({
    item,
    originalId,
    searchId: undefined,
  }));
  const completed = new Array(requestBody.batch.length);

  while (pending.length > 0) {
    if (Date.now() >= deadline) {
      fail(`Kibana search did not finish within 120 seconds`, 1);
    }

    const pollBody = {
      batch: pending.map(({ item, searchId }) => ({
        ...item,
        request: {
          ...item.request,
          ...(searchId ? { id: searchId } : {}),
        },
      })),
    };
    const response = postBsearch(options, pollBody);
    const records = decodeResponse(response);
    const seen = new Set();
    const nextPending = [];

    for (const record of records) {
      const pendingIndex = Number(record?.id);
      const current = pending[pendingIndex];
      if (!current) {
        fail(`Kibana returned an unexpected batch id: ${record?.id}`, 1);
      }
      seen.add(pendingIndex);
      if (record.error) {
        fail(`Kibana returned an error: ${JSON.stringify(record.error)}`, 1);
      }
      if (record?.result?.isRunning) {
        if (!record.result.id) {
          fail(`Kibana returned isRunning=true without a search id`, 1);
        }
        nextPending.push({
          ...current,
          searchId: record.result.id,
        });
      } else {
        completed[current.originalId] = {
          ...record,
          id: current.originalId,
        };
      }
    }

    if (seen.size !== pending.length) {
      fail(
        `Kibana returned ${seen.size} batch result(s), expected ${pending.length}`,
        1,
      );
    }
    pending = nextPending;
    if (pending.length > 0) {
      sleep(100);
    }
  }

  return completed;
}

function rawResponse(record) {
  return record?.result?.rawResponse;
}

function hitTotal(value) {
  if (typeof value === "number") {
    return value;
  }
  if (value && typeof value.value === "number") {
    return value.value;
  }
  return undefined;
}

function collectHits(records) {
  const hits = new Map();
  let total;

  for (const record of records) {
    if (record?.error) {
      fail(`Kibana returned an error: ${JSON.stringify(record.error)}`, 1);
    }
    const response = rawResponse(record);
    const responseTotal = hitTotal(response?.hits?.total);
    if (responseTotal !== undefined) {
      total = total === undefined ? responseTotal : Math.max(total, responseTotal);
    }
    for (const hit of response?.hits?.hits ?? []) {
      const fields = hit.fields ?? {};
      const fallbackKey = [
        fields["@timestamp"]?.[0],
        fields["kubernetes.pod_name"]?.[0],
        fields["message.msg"]?.[0],
      ].join("\u0000");
      hits.set(`${hit._index ?? ""}/${hit._id ?? fallbackKey}`, hit);
    }
  }

  return { hits: [...hits.values()], total: total ?? hits.size };
}

function firstField(fields, name, fallback = "") {
  const value = fields?.[name];
  return Array.isArray(value) ? (value[0] ?? fallback) : (value ?? fallback);
}

function localTimestamp(value, timeZone) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value || "unknown-time";
  }
  return new Intl.DateTimeFormat("sv-SE", {
    timeZone,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    fractionalSecondDigits: 3,
    hourCycle: "h23",
  })
    .format(date)
    .replace(/,(\d{3})$/, ".$1");
}

function countBy(values) {
  const counts = new Map();
  for (const value of values) {
    const key = value || "unknown";
    counts.set(key, (counts.get(key) ?? 0) + 1);
  }
  return [...counts.entries()].sort((a, b) => b[1] - a[1]);
}

function formatTimeline(records, options) {
  const collected = collectHits(records);
  const direction = options.order === "asc" ? 1 : -1;
  collected.hits.sort((left, right) => {
    const leftTime = Date.parse(firstField(left.fields, "@timestamp")) || 0;
    const rightTime = Date.parse(firstField(right.fields, "@timestamp")) || 0;
    return (leftTime - rightTime) * direction;
  });

  const levels = countBy(
    collected.hits.map((hit) => firstField(hit.fields, "level", "unknown")),
  );
  const containers = countBy(
    collected.hits.map((hit) =>
      firstField(hit.fields, "kubernetes.container_name", "unknown"),
    ),
  );

  const lines = [
    `cluster=${options.cluster} ` +
      `${options.game ? `game=${options.game} ` : ""}` +
      `matches=${collected.total} ` +
      `returned=${collected.hits.length} ` +
      `truncated=${collected.total > collected.hits.length} ` +
      `range=${options.start}..${options.end}`,
    `levels=${levels.map(([name, count]) => `${name}:${count}`).join(",")}`,
    `containers=${containers.map(([name, count]) => `${name}:${count}`).join(",")}`,
  ];

  for (const hit of collected.hits) {
    const fields = hit.fields ?? {};
    const timestamp = localTimestamp(
      firstField(fields, "@timestamp"),
      options.timeZone,
    );
    const level = String(firstField(fields, "level", "unknown")).toUpperCase();
    const container = firstField(
      fields,
      "kubernetes.container_name",
      "unknown-container",
    );
    const pod = firstField(fields, "kubernetes.pod_name", "unknown-pod");
    const message = String(firstField(fields, "message.msg", ""));
    const indentedMessage = message.replace(/\n/g, "\n    ");
    lines.push(`${timestamp} [${level}] ${container}/${pod} ${indentedMessage}`);

    if (options.includeStacktrace) {
      const stacktrace = firstField(fields, "stacktrace");
      if (stacktrace && !message.includes(stacktrace)) {
        lines.push(`    ${String(stacktrace).replace(/\n/g, "\n    ")}`);
      }
    }
  }

  return lines.join("\n");
}

function formatRecords(records, options) {
  switch (options.format) {
    case "ndjson":
      return records.map((record) => JSON.stringify(record)).join("\n");
    case "json":
      return JSON.stringify(records, null, 2);
    case "timeline":
      return formatTimeline(records, options);
  }
}

function defaultOutputPath(options) {
  const outputDirectory = join(tmpdir(), "kibana-log-query-results");
  mkdirSync(outputDirectory, { recursive: true, mode: 0o700 });
  chmodSync(outputDirectory, 0o700);
  const timestamp = new Date().toISOString().replace(/[:.]/g, "-");
  const extension = options.format === "timeline" ? "log" : options.format;
  return join(
    outputDirectory,
    `kibana-logs-${options.cluster}-${timestamp}-${randomUUID().slice(0, 8)}.${extension}`,
  );
}

function prepareOutputPath(options) {
  let outputPath;
  try {
    outputPath = options.output
      ? resolve(options.output)
      : defaultOutputPath(options);
    mkdirSync(dirname(outputPath), { recursive: true, mode: 0o700 });
  } catch (error) {
    fail(`could not prepare output path: ${error.message}`, 1);
  }
  if (existsSync(outputPath)) {
    fail(`output file already exists: ${outputPath}`);
  }
  return outputPath;
}

function writeResults(records, options, outputPath) {
  const content = formatRecords(records, options);

  try {
    writeFileSync(outputPath, `${content}\n`, {
      encoding: "utf8",
      flag: "wx",
      mode: 0o600,
    });
    chmodSync(outputPath, 0o600);
  } catch (error) {
    if (error?.code === "EEXIST") {
      fail(`output file already exists: ${outputPath}`);
    }
    fail(`could not write output file ${outputPath}: ${error.message}`, 1);
  }

  return outputPath;
}

const options = parseArgs(process.argv.slice(2));
const outputPath = prepareOutputPath(options);
const records = executeSearch(options, buildRequest(options));

if (records.length === 0) {
  fail("Kibana returned an empty response", 1);
}

writeResults(records, options, outputPath);
console.log(`output_file=${outputPath}`);
