# GitLab MR 创建、更新及合并审查

`ai-review serve-lark-codex` 接收 `Merge Request Hook`，目标分支按 `main` 精确匹配：

- 创建、重新打开和更新：`action=open/reopen/update` 且 `state=opened` 时审查该源提交。
  更新包括向 MR 源分支推送代码；同一提交的重复通知或标题、描述等编辑按版本去重。
- 合并：`action=merge` 且 `state=merged` 时独立审查实际合并结果，包含冲突解决。
- 其他目标分支、关闭、审批、已合并后的普通更新和 Tag/Push Hook 返回 `200 ignored`。

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
- 核验 GitLab 项目、事件阶段、分支和固定 diff refs。合并前检查事件对应版本的完整
  MR base/head 差异，不能只检查本次推送的最后一个提交；普通合并检查第一父提交到合并
  结果的差异，包含冲突解决；压缩合并核验 squash 结果；多提交快进合并使用完整
  MR base/head 差异，不能只看最后一个提交。不会使用目标分支的最新 HEAD。
- 新建 MR 的 diff refs 异步生成，解析脚本最多尝试三次；排队期间源分支已更新时，
  按事件 head SHA 查找历史 diff 版本，最多十页、每页 100 个。合并前事件即使之后
  已合并或关闭，也只审事件对应源版本。无法核验分支或找到版本时报告限制。
- 内部依赖按精确版本和 `replace` 读取；模块变更只比较相关依赖的影响。
  必要时可将 `game-common`、`slot-app` 对应版本的源码拉取到本地隔离目录，
  核验模块及提交后仅追踪相关调用链，不切换共享工作区或使用最新分支替代。
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
- 按需读取精确版本的 Nova 依赖；相关路径需要且本地缺少源码时，可定向拉取
  `game-common`、`slot-app`，其余依赖继续读取已有源码。缺失本地 `replace` 且无法
  核验提交时不猜测版本。必要的局部测试先排查 `init`、`TestMain` 和 setup，
  保证不会间接启动游戏或生成数据。
- 中文正文先交付确认问题，再列待补证项、基线、实际覆盖与验证限制，不生成报告附件。
  问题需有行号、前后差异、触发条件、调用链、影响与修复方向，资金问题说明损失链路。

## Lark 和并发

MR 审查与人工任务共享 worker 池，默认五个任务并行。创建/更新/重新打开事件按
项目、MR、目标分支和源提交 SHA 在七天内去重；源提交变化后重新审查。同一项目/MR
的合并事件独立去重并兼容旧状态。排队中和运行中的任务也去重。排队成功立即返回 `202`，无需
等待分析结束。队列满返回 `429` 和 `Retry-After: 30`。

“MR 创建审查已开始”“MR 更新审查已开始”等作为 Lark 线程根消息，结果或失败提示回复到该线程，并保存
session。用户回复线程内消息并真正 `@机器人` 即可追问。同线程串行并复用上下文。
不同源版本及合并阶段使用独立的线程和 session；旧线程追问保持原版本和阶段。
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

已有 **Merge request events** 订阅会包含创建、更新和合并，无需额外启用 Push events。
GitLab 的“测试”按钮如果发送目标为 main 的有效 open/update 事件，也会触发真实审查。
验证接收端可使用缺少 IID 的负向请求，避免为了测试发送真实审查或通知。

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
