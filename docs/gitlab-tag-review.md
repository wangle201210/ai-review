# Tag 自动审查已迁移为 MR 合并审查

新的自动审查只在 MR 被合并时触发，检查该 MR 的变更逻辑及受影响逻辑。
配置、Webhook 迁移和日志说明见 [MR 合并审查](gitlab-mr-review.md)。

旧 `GITLAB_TAG_REVIEW__*` 配置和 `/webhooks/gitlab/tag` URL 保留兼容，但 Tag
事件不再触发审查；GitLab 必须订阅 **Merge request events**。

旧 Tag 审查的 Lark 线程仍可通过 `@机器人` 复用原 session，并继续使用
[`nova-tag-fund-risk-review`](../skills/nova-tag-fund-risk-review/SKILL.md)。
