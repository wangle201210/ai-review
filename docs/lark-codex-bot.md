# Lark Codex 机器人

`ai-review serve-lark-codex` 使用 Lark 长连接接收群聊消息，把用户当前消息和被回复
消息交给现有 Codex HTTP 服务，再把 Codex 最终结果回复到同一消息线程。

这层只负责 Lark 收发、队列、去重和 `session_id` 映射。它不会解析告警字段，不会
直接查询 VictoriaLogs、修改代码或调用 GitLab。真正的日志分析、修复、验证和创建 MR
仍由 Codex CLI 及 `$nova-incident-remediation` skill 完成。

## 消息流程

1. 用户在群里回复一条告警或日志消息。
2. 用户在回复中 `@机器人`，并写本次要求，例如“查明原因，修复并提交 MR”。
3. 机器人立即回复“已收到”，然后把两条消息合成一个 Codex turn。
4. Codex 完成后，机器人把最终结果回复到用户的消息。
5. 同一根消息下的后续 `@机器人` 会自动携带之前的 `session_id`。

如果 Codex 超时但已创建 session，机器人会保存该 ID，并提示用户在同一线程发送
一条新的消息（例如“继续”）。机器人不会自动重跑；只有用户明确发送新消息后，
下一次调用才会通过 `codex exec resume` 继续原 session。

如果 Codex 最终回答触发 `flagged for possible cybersecurity risk`，Codex HTTP 网关
会自动恢复原 session 一次，强调这是公司自有授权项目的防御性检查，并继续要求给出
本地或隔离环境中的具体复现步骤。第二次仍被拦截时，机器人保存 session 并提示用户
在原线程发送“继续”。

GitLab MR 审查同样建立 Lark 线程和 session 映射。机器人把最终审查结果回复到
“MR 合并审查已开始”消息下；用户回复线程内任意消息并 `@机器人` 时，会恢复
原 MR 审查 session，并继续使用 `$nova-mr-impact-review`，不会进入事故修复
流程。不同 MR 审查使用不同 session。

服务默认运行五个 Codex worker。不同群或不同根消息可以并行执行；同一根消息通过
线程顺序锁保持串行，前一轮保存 `session_id` 后下一轮才开始。启用 GitLab MR
审查后，Webhook 任务也使用同一 worker 池。队列满时群消息会提示用户稍后重试，
Webhook 返回 `429`。

## Lark 开发者后台

该功能需要企业自建应用，不是只能发消息的群自定义机器人。

1. 为应用启用机器人能力，并把机器人加入目标群。
2. 在“事件与回调”中选择“使用长连接接收事件”。
3. 订阅事件 `im.message.receive_v1`。
4. 申请并发布包含下列应用身份权限的新版本：

| 权限 | 用途 |
|---|---|
| `im:message.group_at_msg:readonly` | 接收群聊中用户 @ 机器人的消息 |
| `im:message:send_as_bot` | 回复确认和 Codex 结果 |
| `im:message:readonly` | 调用获取指定消息接口 |
| `im:message.group_msg` | 读取群聊中被回复的告警；这是敏感权限 |
| `application:bot.basic_info:read` | 识别哪一个 mention 是当前机器人 |

如果缺少 `im:message.group_msg`，服务可以收到用户的 @ 消息，但获取父消息 API 会
失败，Codex 因而拿不到告警正文。

## 启动

先确保同机的 Codex HTTP 服务可用：

```bash
curl http://127.0.0.1:8787/healthz
```

再通过受保护的环境文件启动 Lark 适配器：

```bash
export LARK__APP_ID=cli_xxx
export LARK__APP_SECRET=replace_me
export LARK__BASE_URL=https://open.larksuite.com
export LARK__STATE_PATH=/var/lib/ai-review/lark-state.json
export LARK__CODEX_URL=http://127.0.0.1:8787/v1/codex
export LARK__CODEX_AUTH_TOKEN=

ai-review serve-lark-codex
```

中国版飞书应用把 `LARK__BASE_URL` 改为 `https://open.feishu.cn`。App Secret 不应
写进仓库、命令行参数或 systemd unit；使用权限为 `0600` 的
`EnvironmentFile`。

## systemd 示例

环境文件 `/etc/ai-review/lark-codex.env`：

```bash
LARK__APP_ID=cli_xxx
LARK__APP_SECRET=replace_me
LARK__BASE_URL=https://open.larksuite.com
LARK__STATE_PATH=/var/lib/ai-review/lark-state.json
LARK__CODEX_URL=http://127.0.0.1:8787/v1/codex
LARK__CODEX_AUTH_TOKEN=
LARK__QUEUE_SIZE=32
LARK__WORKER_COUNT=5
LARK__REQUIRE_REPLY=true
GITLAB_MR_REVIEW__ENABLED=false
```

服务文件可以直接使用
[`deploy/systemd/ai-review-lark-codex.service`](../deploy/systemd/ai-review-lark-codex.service)。
它等价于下面的核心配置，并额外限制文件写入范围：

```ini
[Unit]
Description=AI Review Lark Codex Bot
After=network-online.target ai-review-codex-http.service
Wants=network-online.target
Requires=ai-review-codex-http.service

[Service]
Type=simple
User=root
WorkingDirectory=/root/game-play
Environment=HOME=/root
EnvironmentFile=/etc/ai-review/lark-codex.env
ExecStart=/usr/local/bin/ai-review serve-lark-codex
Restart=always
RestartSec=5
MemoryMax=256M
CPUQuota=50%

[Install]
WantedBy=multi-user.target
```

```bash
chmod 600 /etc/ai-review/lark-codex.env
mkdir -p /var/lib/ai-review
chmod 700 /var/lib/ai-review
systemctl daemon-reload
systemctl enable --now ai-review-lark-codex.service
```

## 配置

| 环境变量 | 默认值 | 说明 |
|---|---:|---|
| `LARK__APP_ID` | 无 | 必填，应用 App ID |
| `LARK__APP_SECRET` | 无 | 必填，应用 App Secret |
| `LARK__BASE_URL` | `https://open.larksuite.com` | Lark/飞书 Open API 地址 |
| `LARK__ALLOWED_CHAT_IDS` | 无 | 可选，逗号分隔的群 ID 白名单；空表示所有群 |
| `LARK__STATE_PATH` | `.ai-review-lark-state.json` | 会话映射和消息去重状态 |
| `LARK__QUEUE_SIZE` | `32` | 等待处理的最大任务数 |
| `LARK__WORKER_COUNT` | `5` | 并行处理不同线程的 worker 数；同一线程仍串行 |
| `LARK__REQUIRE_REPLY` | `true` | 是否必须回复一条消息后才能发起任务 |
| `LARK__CODEX_URL` | `http://127.0.0.1:8787/v1/codex` | Codex HTTP API |
| `LARK__CODEX_AUTH_TOKEN` | 无 | Codex HTTP Bearer Token；空表示不发送鉴权头 |
| `LARK__CODEX_TIMEOUT_SECONDS` | `3660` | 等待单个 Codex turn 的超时；应略大于服务端 turn 超时 |
| `LARK__BUSY_RETRY_SECONDS` | `5` | Codex 返回 `409/429` 后的重试间隔 |
| `LARK__MAX_PROMPT_BYTES` | `49152` | 合成提示词的最大字节数 |

GitLab MR 审查配置见 [`gitlab-tag-review.md`](gitlab-tag-review.md)。目标群由
`GITLAB_MR_REVIEW__LARK_CHAT_ID` 独立配置，因此后续可以在不改变日常 Bug 修复群
的情况下切换到专用审查群。

状态文件以 `0600` 原子写入。已处理的消息 ID 和 Tag 事件保留 7 天用于去重；线程
到 Codex session 的映射及线程类型会跨服务重启保留。

## 日志

查看机器人收发和队列状态：

```bash
journalctl -u ai-review-lark-codex.service -f -o cat
```

查看 Codex 的完整输入、执行过程和最终消息：

```bash
journalctl -u ai-review-codex-http.service -f -o cat
```

Lark 服务只记录消息 ID、群 ID、session ID、队列深度和错误，不记录 App Secret。
