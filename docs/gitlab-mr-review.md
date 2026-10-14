# GitLab MR 合并后增量审查

`ai-review serve-lark-codex` 接收 `Merge Request Hook`，只在
`object_attributes.action=merge`、`state=merged` 且 `target_branch=main` 时触发审查。
合并到其他分支（包括从 `main` 合并到其他分支）、MR 创建、更新、审批、关闭以及
Tag Push 事件均返回 `200 ignored`，不会入队或调用 Codex。目标分支按 `main` 精确匹配。

Webhook 只校验事件、排队和通知；源码准备、差异核验和分析由
[`nova-mr-impact-review`](../skills/nova-mr-impact-review/SKILL.md) 完成。
其中 `nova/game-play/gamev3` 的分析和报告使用
[`code-audit`](../skills/code-audit/SKILL.md) 及完整知识库；按项目路径精确匹配，
其他仓库继续使用原审查流程。MR 固定版本仍由 `nova-mr-impact-review` 核验。

## 审查范围

- 检查该 MR 的变更逻辑及可能受影响的调用者、被调用者、共享状态、配置和依赖。
- 保留下注/撤销、余额一致性、策略异常、重连结算、规则/玩法资金风险和空指针检查。
- 以本次变更为入口；报告新增、暴露或加剧的问题，指出变更到影响的因果路径。
  可以读取和报告未修改但受影响的代码，不进行全仓扫描。
- 核验 GitLab 项目、MR 合并状态和固定 diff refs。普通合并检查第一父提交到合并
  结果的差异，包含冲突解决；压缩合并核验 squash 结果；多提交快进合并使用完整
  MR base/head 差异，不能只看最后一个提交。不会使用目标分支的最新 HEAD。
- 内部依赖按精确版本和 `replace` 读取；模块变更只比较相关依赖的影响。
- 默认不执行全量测试。仅在必要时做定向验证；小 MR 直接检查，多个独立影响路径
  最多使用五个只读 subagents。每个任务使用隔离 checkout/worktree。
- 无法确定基线或源码不完整时报告原因，不退回全量审查或声称没有问题。
- 只读审查；不修改代码、创建 MR、构建或部署。

### gamev3 专项规则

gamev3 以 `code-audit` 的规则替代上述默认分析、依赖、验证和报告要求：

- 从变更追踪实际玩法与调控、成本与奖励权益、扣费派奖、幂等、跨局清理、恢复、
  奖池、配置使用及运行时；逐项核实变更归因、真实触发、正确行为、已有防护和影响。
- 先列文件再过滤 `tools/`、`panelDB/`、其他生成数据及 GM 专用逻辑，再读范围内补丁。
  不读取或生成牌库，不启动游戏，不做分布/RTP 验证，不评判数值配置内容。
- 只按需读取精确版本的 Nova 依赖，不主动下载缺失依赖；必要的局部测试先排查
  `init`、`TestMain` 和 setup，保证不会间接启动游戏或生成数据。
- 中文正文先交付确认问题，再列待补证项、基线、实际覆盖与验证限制，不生成报告附件。
  问题需有行号、前后差异、触发条件、调用链、影响与修复方向，资金问题说明损失链路。

## Lark 和并发

MR 审查与人工任务共享 worker 池，默认五个任务并行。相同项目和 MR 的重复合并
事件在七天内去重；排队中和运行中的任务也去重。排队成功立即返回 `202`，无需
等待分析结束。队列满返回 `429` 和 `Retry-After: 30`。

“MR 合并审查已开始”作为 Lark 线程根消息，结果或失败提示回复到该线程，并保存
session。用户回复线程内消息并真正 `@机器人` 即可追问。同线程串行并复用上下文。
gamev3 线程持久化专项审查类型，成功、超时或未完成后续问及服务重启后继续使用
`code-audit`。升级前的 MR session 由技能依据已核验的项目路径选择专项流程。
旧 Tag 审查线程仍按原 Tag skill 续接，不会误进入 MR 或事故修复流程。

## 配置和迁移

```ini
GITLAB_MR_REVIEW__ENABLED=true
GITLAB_MR_REVIEW__LISTEN_ADDR=0.0.0.0:8788
GITLAB_MR_REVIEW__SECRET=
GITLAB_MR_REVIEW__LARK_CHAT_ID=oc_xxx
GITLAB_MR_REVIEW__ALLOWED_HOST=git.easycodesource.com
GITLAB_MR_REVIEW__ALLOWED_NAMESPACE=nova/game-play
GITLAB_MR_REVIEW__MAX_REQUEST_BYTES=65536
```

默认关闭，默认监听 `127.0.0.1:8788`。Secret 可选，非空时至少 32 字节；为空时不
检查 `X-Gitlab-Token`。沿用原来的群、监听地址、队列、超时和鉴权设置即可。

兼容旧 `GITLAB_TAG_REVIEW__*` 环境变量与 `gitlab_tag_review` YAML 配置。
对应的新环境变量优先于旧环境变量；新 YAML 字段覆盖同名旧 YAML 字段。
旧 URL `/webhooks/gitlab/tag` 也是 MR 处理入口的兼容别名，已不再触发 Tag 审查。
**仅升级服务不会更改 GitLab 的事件订阅，必须把 Webhook 切换为 MR 事件。**

在 GitLab 项目 Webhook 设置中修改现有条目：

- URL：`http://<服务器地址>:8788/webhooks/gitlab/mr`，有 HTTPS 入口时使用对应地址。
- Trigger：勾选 **Merge request events**，取消 **Tag push events**。
- Secret token 和 SSL verification 与部署的鉴权及 HTTP/HTTPS 方式保持一致。
  GitLab 更改 Webhook URL 时会重置 Secret；如果服务配置了非空 Secret，需同时重新填写。

GitLab 会发送多种 MR 事件，服务只接受其中的合并动作。GitLab 的“测试”按钮若发送
的是未合并 MR，返回 `ignored` 是预期行为。不要为了验证接收端而合并无关 MR。

将 `nova-mr-impact-review`（包括 scripts）和 `code-audit`（包括整个 `知识库/`
及 `agents/`）一起安装到运行 Codex 的用户的 `~/.codex/skills/`。
只复制 `SKILL.md` 会丢失专项规则。gamev3 缺少 `code-audit` 时报告缺失，不退回旧流程。
元数据脚本需要可读取项目/MR 的 GitLab API 凭据：使用 `GITLAB_TOKEN`，或将现有
凭据保存在 `/etc/ai-review/gitlab-api-token`（权限 `0600`，属主为运行用户）。也可用
`GITLAB_TOKEN_FILE` 指定文件。脚本只对固定 GitLab 主机发 GET 请求，不输出凭据；
HTTP 网关仍不向 Codex 环境转发服务 Token。凭据不可提交到仓库。

## 日志

```bash
journalctl -u ai-review-lark-codex.service -f -o cat
journalctl -u ai-review-codex-http.service -f -o cat
```

Webhook 日志前缀为 `[gitlab-mr-review]`。健康检查为 `GET /healthz`。

事件与差异字段参考：[GitLab Webhook events](https://docs.gitlab.com/user/project/integrations/webhook_events/#merge-request-events)、
[Merge requests API](https://docs.gitlab.com/api/merge_requests/)。
