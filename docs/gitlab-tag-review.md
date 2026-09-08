# GitLab Tag 自动资金风险审查

`ai-review serve-lark-codex` 可以接收 GitLab `Tag Push Hook`，把经过校验的项目、
Tag 和 Commit 信息排入现有 Codex 队列，并将最终审查报告主动发送到指定 Lark 群。
Webhook 只做鉴权、事件解析、范围校验、排队和通知；实际源码审查由
`$nova-tag-fund-risk-review` 完成。

## 行为

- 创建或更新 Tag 时审查该 Tag 对应的完整代码，不只审查相对上一个 Tag 的 diff。
- 删除 Tag 时返回 `200 ignored`，不触发 Codex。
- 同一项目、Tag 和 Commit 的重复事件在 7 天内不会重复执行。
- Webhook 成功入队后立即返回 `202`，不等待 Codex 完成。
- Tag 审查与 Lark 人工任务共享单 worker 队列，避免同时切换或修改工作区。
- Codex 使用隔离的临时 checkout，并验证 Tag 指向 Webhook 提供的 Commit。
- 审查只报告可能导致资金损失的游戏规则漏洞、玩法设计问题或调控策略问题。
- 审查不会修改代码、提交分支、创建 MR、触发构建或发布版本。
- 开始、成功或失败状态都会主动发送到配置的 Lark 群。

## 配置

在 Lark 服务的受保护环境文件中配置：

```ini
GITLAB_TAG_REVIEW__ENABLED=true
GITLAB_TAG_REVIEW__LISTEN_ADDR=0.0.0.0:8788
GITLAB_TAG_REVIEW__SECRET=<至少 32 字节的随机值>
GITLAB_TAG_REVIEW__LARK_CHAT_ID=oc_xxx
GITLAB_TAG_REVIEW__ALLOWED_HOST=git.easycodesource.com
GITLAB_TAG_REVIEW__ALLOWED_NAMESPACE=nova/game-play
GITLAB_TAG_REVIEW__MAX_REQUEST_BYTES=65536
```

| 环境变量 | 默认值 | 说明 |
|---|---:|---|
| `GITLAB_TAG_REVIEW__ENABLED` | `false` | 是否启用 Tag Webhook HTTP 服务 |
| `GITLAB_TAG_REVIEW__LISTEN_ADDR` | `127.0.0.1:8788` | Webhook 监听地址 |
| `GITLAB_TAG_REVIEW__SECRET` | 无 | 必填，GitLab `Secret token`，至少 32 字节 |
| `GITLAB_TAG_REVIEW__LARK_CHAT_ID` | 无 | 必填，接收开始状态和审查结果的群 ID |
| `GITLAB_TAG_REVIEW__ALLOWED_HOST` | `git.easycodesource.com` | 允许的 GitLab 主机 |
| `GITLAB_TAG_REVIEW__ALLOWED_NAMESPACE` | `nova/game-play` | 允许触发审查的项目命名空间 |
| `GITLAB_TAG_REVIEW__MAX_REQUEST_BYTES` | `65536` | 最大请求体字节数 |

生成 Webhook Secret：

```bash
openssl rand -hex 32
```

Secret 只保存在权限为 `0600` 的环境文件和 GitLab Webhook 配置中，不应写入仓库、
命令行历史或日志。

## GitLab Webhook

在项目或 `nova/game-play` Group 的 Webhook 设置中填写：

- URL：`https://<公开域名>/webhooks/gitlab/tag`
- Secret token：与 `GITLAB_TAG_REVIEW__SECRET` 相同
- Trigger：只启用 `Tag push events`
- SSL verification：使用有效 HTTPS 证书时启用

如果直接暴露独立端口，URL 形如：

```text
http://<服务器地址>:8788/webhooks/gitlab/tag
```

Group Webhook 会覆盖组内项目，但 GitLab 要求相应版本授权且操作者拥有 Group Owner
或管理员权限；否则应在各项目配置 Project Webhook。GitLab 单次 push 默认超过三个
Tag 时可能不发送任何 Tag Hook，应避免批量创建大量 Tag。

Webhook 健康检查：

```bash
curl http://127.0.0.1:8788/healthz
```

GitLab 成功投递时收到类似响应：

```json
{
  "status": "accepted",
  "project": "nova/game-play/kraken",
  "tag": "version/v2.65.5",
  "commit_sha": "82b3d5ae55f7080f1e6022629cdb57bfae7cccc7"
}
```

`duplicate` 表示相同事件已经排队或处理；`ignored` 表示 Tag 被删除；队列满时返回
`429` 和 `Retry-After: 30`。

## 日志

查看 Webhook 接收、排队和 Lark 通知：

```bash
journalctl -u ai-review-lark-codex.service -f -o cat |
  grep --line-buffered '\[gitlab-tag-review\]'
```

查看 Codex 输入及完整执行过程：

```bash
journalctl -u ai-review-codex-http.service -f -o cat
```
