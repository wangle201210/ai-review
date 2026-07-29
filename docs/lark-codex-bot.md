# Lark Codex 机器人

`ai-review serve-lark-codex` 使用 Lark 长连接接收群聊消息，把用户当前消息和被回复
消息交给现有 Codex HTTP 服务，再把 Codex 最终结果回复到同一消息线程。

这层只负责 Lark 收发、队列、去重和 `session_id` 映射。它不会解析告警字段，不会
直接查询 Kibana、修改代码或调用 GitLab。真正的日志分析、修复、验证和创建 MR
仍由 Codex CLI 及 `$nova-incident-remediation` skill 完成。

## 消息流程

1. 用户在群里回复一条告警或日志消息。
2. 用户在回复中 `@机器人`，并写本次要求，例如“查明原因，修复并提交 MR”。
3. 机器人立即回复“已收到”，然后把两条消息合成一个 Codex turn。
4. Codex 完成后，机器人把最终结果回复到用户的消息。
5. 同一根消息下的后续 `@机器人` 会自动携带之前的 `session_id`。

服务内部只有一个 Codex worker。即使多个群同时发任务，也不会在这台服务器上并发
启动多个 Codex CLI。队列满时会直接提示用户稍后重试。

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
LARK__REQUIRE_REPLY=true
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
| `LARK__REQUIRE_REPLY` | `true` | 是否必须回复一条消息后才能发起任务 |
| `LARK__CODEX_URL` | `http://127.0.0.1:8787/v1/codex` | Codex HTTP API |
| `LARK__CODEX_AUTH_TOKEN` | 无 | Codex HTTP Bearer Token；空表示不发送鉴权头 |
| `LARK__CODEX_TIMEOUT_SECONDS` | `1860` | 等待单个 Codex turn 的超时 |
| `LARK__BUSY_RETRY_SECONDS` | `5` | Codex 返回 `409/429` 后的重试间隔 |
| `LARK__MAX_PROMPT_BYTES` | `49152` | 合成提示词的最大字节数 |

状态文件以 `0600` 原子写入。已处理的消息 ID 保留 7 天，用于抵御 Lark 重复推送；
线程到 Codex session 的映射会跨服务重启保留。

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
