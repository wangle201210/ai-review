# Codex HTTP 服务

`ai-review serve-codex` 把服务器上的 Codex CLI 包装成同步 HTTP 接口。每个请求
对应一个 Codex turn；新请求创建会话，携带 `session_id` 的请求恢复已有会话。
会话记录由 Codex 持久化在运行用户的 `CODEX_HOME` 中，因此 HTTP 服务重启后仍可
恢复。

## 责任边界

HTTP 层只处理 JSON、可选鉴权、请求大小、并发、超时、Codex 进程和
`session_id`。请求中的 `message` 会原样写入 Codex CLI 的标准输入；服务不会解析
应用、时间、集群或日志，不会查询 Kibana，也不会调用 GitLab API。

告警定位和修复流程由 Codex skill 负责。本项目包含四个可部署 skills：
[`nova-incident-remediation`](../skills/nova-incident-remediation/SKILL.md) 负责编排，
[`kibana-log-query`](../skills/kibana-log-query/SKILL.md) 负责查询日志，
[`nova-game-play-code-analysis`](../skills/nova-game-play-code-analysis/SKILL.md)
负责准备项目并分析源码，
[`jenkins-trigger-build`](../skills/jenkins-trigger-build/SKILL.md) 负责预览并触发
Nova Jenkins 构建。将它们安装到运行 HTTP 服务的同一用户下：

```bash
install -d ~/.codex/skills
cp -R \
  skills/kibana-log-query \
  skills/jenkins-trigger-build \
  skills/nova-game-play-code-analysis \
  skills/nova-incident-remediation \
  ~/.codex/skills/
```

该 skill 让 Codex 查询日志、检查源码、完成最小修复和窄范围验证，再使用已有 SSH
Git 权限及 GitLab push options 推送分支并创建 MR。这个流程不需要把
`GITLAB_TOKEN` 传给 Codex。

Jenkins skill 是独立的外部操作能力。事故修复、推送分支或创建 MR 本身不会触发
Jenkins；只有用户明确要求 build、deploy、rebuild 或 release 时，Codex 才能在
无网络预览后提交构建请求。未指定构建分支时使用 `main`。

### Jenkins 凭据

触发 Jenkins 时，Codex 子进程必须能读取 `JENKINS_USER` 和 `JENKINS_TOKEN`。
systemd 不会加载 `.bashrc`，应把凭据写入只允许服务用户读取的环境文件，例如
`/etc/ai-review/codex.env`：

```ini
JENKINS_USER=<jenkins-user>
JENKINS_TOKEN=<jenkins-api-token>
```

设置文件权限为 `0600`，并在 Codex HTTP unit 的 `[Service]` 中加载：

```ini
EnvironmentFile=/etc/ai-review/codex.env
```

修改后运行 `systemctl daemon-reload` 并重启 Codex HTTP 服务。可选变量包括
`JENKINS_URL`、`JENKINS_DEPLOY_ENV` 和 `JENKINS_OPERATION_TYPE`。这些变量会传给
Codex 及其命令，因此只能使用权限受限的 Jenkins API token，并限制服务账号和
journal 的访问权限。不要把 token 写入仓库、聊天消息或命令参数。

## 前置条件

1. 安装 `codex`、Node.js、Python 3.10+ 和 `curl`，并确保服务进程可以在
   `PATH` 中找到它们。
2. 使用运行 HTTP 服务的同一个系统用户完成 `codex login`。
3. 准备一个专用、低权限的系统用户，并只授予目标工作目录所需权限。
4. 需要 HTTP 鉴权时，生成至少 32 字节的随机 Bearer Token。

不要以 root 用户把此服务直接暴露到网络。Codex 可以在工作目录内读写文件和执行
命令，HTTP Token 也不能替代操作系统权限隔离。

## 启动

```bash
export HTTP__AUTH_TOKEN="$(openssl rand -hex 32)"
export CODEX__WORK_DIR=/srv/my-project
export CODEX__SANDBOX=workspace-write

ai-review serve-codex
```

`HTTP__AUTH_TOKEN` 为空或未设置时，服务不检查 `Authorization` 请求头。此模式
只能用于 `127.0.0.1`、SSH 隧道或其他已经完成访问控制的可信网络边界。

默认监听 `127.0.0.1:8787`。健康检查不需要鉴权：

```bash
curl http://127.0.0.1:8787/healthz
```

## HTTP API

### 新建会话

```http
POST /v1/codex
Authorization: Bearer <token>
Content-Type: application/json

{
  "message": "检查当前项目并指出最重要的问题"
}
```

成功响应：

```json
{
  "session_id": "019abcde-1234-7000-8000-0123456789ab",
  "message": "检查结果……",
  "status": "completed",
  "usage": {
    "input_tokens": 1234,
    "cached_input_tokens": 800,
    "output_tokens": 120,
    "reasoning_output_tokens": 0
  }
}
```

### 恢复会话

```http
POST /v1/codex
Authorization: Bearer <token>
Content-Type: application/json

{
  "message": "继续修复第一个问题",
  "session_id": "019abcde-1234-7000-8000-0123456789ab"
}
```

同一个 `session_id` 不能同时处理两个请求，发生冲突时返回 `409
session_busy`。达到全局并发上限时返回 `429 server_busy`。
客户端断开或 turn 超时时，服务会终止本次 Codex 调用及其子进程。如果新 turn 在
超时前已经产生 `thread.started` 事件，`504 codex_timeout` 会附带可恢复的
`session_id`：

```json
{
  "error": {
    "code": "codex_timeout",
    "message": "Codex request timed out"
  },
  "session_id": "019abcde-1234-7000-8000-0123456789ab"
}
```

服务不会自动重跑超时任务。调用方应保存这个 ID，并仅在用户明确发起下一条消息时
将它作为 `session_id` 传回。若超时发生在 session 创建之前，响应不包含该字段。

## 查看 Codex 输出

服务会把 Codex CLI 的原始 JSONL stdout 事件和 stderr 实时写入自身日志。每行带有
Codex 进程 PID，分别使用 `[codex-cli stdout pid=...]` 和
`[codex-cli stderr pid=...]` 前缀；进程启动和正常退出也会单独记录。

发送给 Codex 的原始消息使用 `[codex-input pid=...]` 前缀逐行记录。输入、原始
事件和可读进度都可能包含生产日志、源码、命令输出或其他敏感数据。

同时，服务会把 JSONL 事件转成 `[codex-progress pid=...]` 可读进度，包括 Codex
消息、reasoning 摘要、命令执行及输出、文件修改、MCP 工具调用、网页搜索、计划
更新和 turn 结果。原始 JSONL 仍然保留，便于排查未识别字段。

systemd 部署可实时查看完整输出：

```bash
journalctl -u ai-review-codex-http.service -f -o cat
```

日常只查看可读处理过程：

```bash
journalctl -u ai-review-codex-http.service -f -o cat \
  | grep --line-buffered '\[codex-progress'
```

查看指定时间范围：

```bash
journalctl -u ai-review-codex-http.service \
  --since "2026-07-29 02:00:00" \
  --until "2026-07-29 03:00:00" \
  -o cat
```

这些原始事件可能包含 Codex 的最终回复、工具调用、命令输出、文件路径及请求涉及的
业务数据。应按生产日志的敏感级别限制 journal 读取权限和保留时间。

## 从本机访问

建议让服务保持监听回环地址，通过 SSH 隧道访问：

```bash
ssh -N -L 8787:127.0.0.1:8787 user@server
```

然后在本机请求：

```bash
curl http://127.0.0.1:8787/v1/codex \
  -H "Authorization: Bearer $HTTP__AUTH_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"message":"总结当前仓库结构"}'
```

不要把 Codex App Server 或本服务的未加密端口直接开放到公网。如需经过反向代理，
必须启用 TLS、保留 Bearer 鉴权，并把代理读取超时设置为大于 Codex turn 超时。

## 配置

配置可写入 `.ai-review.yaml`，也可通过环境变量覆盖：

| 环境变量 | 默认值 | 说明 |
|---|---:|---|
| `HTTP__LISTEN_ADDR` | `127.0.0.1:8787` | HTTP 监听地址 |
| `HTTP__AUTH_TOKEN` | 无 | 可选；非空时启用 Bearer 鉴权且至少 32 字节 |
| `AI_REVIEW_HTTP_TOKEN` | 无 | `HTTP__AUTH_TOKEN` 的备用名称 |
| `HTTP__MAX_CONCURRENT` | `1` | 全局同时运行的 Codex 请求数 |
| `HTTP__MAX_REQUEST_BYTES` | `65536` | 最大 HTTP 请求体字节数 |
| `CODEX__BINARY` | `codex` | Codex CLI 路径或命令名 |
| `CODEX__WORK_DIR` | 无 | 必填，Codex 唯一工作目录 |
| `CODEX__SANDBOX` | `workspace-write` | `read-only` 或 `workspace-write` |
| `CODEX__TIMEOUT_SECONDS` | `1800` | 单个 turn 超时时间 |
| `CODEX__SKIP_GIT_REPO_CHECK` | `false` | 是否允许在非 Git 目录运行 |
| `CODEX__NETWORK_ACCESS` | `false` | 在 `workspace-write` 沙箱中允许命令访问网络 |

对应的 YAML：

```yaml
http:
  listen_addr: 127.0.0.1:8787
  auth_token: ${AI_REVIEW_HTTP_TOKEN}
  max_concurrent: 1
  max_request_bytes: 65536

codex:
  binary: codex
  work_dir: /srv/my-project
  sandbox: workspace-write
  timeout_seconds: 1800
  skip_git_repo_check: false
  network_access: false
```

服务不会接受客户端传入工作目录、沙箱模式、模型或 CLI 参数。这样可以防止调用方
越过服务端设定的文件权限和资源限制。HTTP Token、VCS Token 和 Claude API Key
也会在启动 Codex 子进程前从环境中移除；Codex 应使用同一系统用户已经保存的登录
状态。新会话和恢复会话都会重新固定服务端配置的工作目录、沙箱和非交互审批策略。
