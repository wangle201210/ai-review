---
name: gitlab-inline-review
description: Post inline review comments to GitLab MR via Discussions API. Requires GITLAB_URL, GITLAB_TOKEN, GITLAB_PROJECT_ID, GITLAB_MR_IID, GITLAB_BASE_SHA, GITLAB_HEAD_SHA, GITLAB_START_SHA environment variables.
---

# GitLab Merge Request Inline Review

Post inline comments to a GitLab MR via the Discussions API using curl.

## Environment Variables

| Variable | Description |
|---|---|
| `GITLAB_URL` | GitLab instance URL (e.g. `https://gitlab.example.com`) |
| `GITLAB_TOKEN` | GitLab Private Token |
| `GITLAB_PROJECT_ID` | Project ID |
| `GITLAB_MR_IID` | Merge Request IID |
| `GITLAB_BASE_SHA` | diff_refs base SHA |
| `GITLAB_HEAD_SHA` | diff_refs head SHA |
| `GITLAB_START_SHA` | diff_refs start SHA |

## Post a single inline comment

```bash
curl -sf --request POST \
  --header "PRIVATE-TOKEN: $GITLAB_TOKEN" \
  --header "Content-Type: application/json" \
  --data '{
    "body": "COMMENT_BODY",
    "position": {
      "base_sha": "'"$GITLAB_BASE_SHA"'",
      "head_sha": "'"$GITLAB_HEAD_SHA"'",
      "start_sha": "'"$GITLAB_START_SHA"'",
      "position_type": "text",
      "new_path": "FILE_PATH",
      "old_path": "FILE_PATH",
      "new_line": LINE_NUMBER
    }
  }' \
  "$GITLAB_URL/api/v4/projects/$GITLAB_PROJECT_ID/merge_requests/$GITLAB_MR_IID/discussions"
```

## Batch posting

Use the helper script `scripts/post_comment.sh` for each comment:

```bash
bash scripts/post_comment.sh "FILE_PATH" LINE_NUMBER "COMMENT_BODY"
```

## Verify posted comments

```bash
curl -sf --header "PRIVATE-TOKEN: $GITLAB_TOKEN" \
  "$GITLAB_URL/api/v4/projects/$GITLAB_PROJECT_ID/merge_requests/$GITLAB_MR_IID/discussions"
```

## Rules

1. `new_path` and `old_path` must match the file path in the diff exactly
2. `new_line` is the new-file line number (derived from `@@` hunk headers); only comment on added lines (`+`)
3. Include ````suggestion` code blocks in body when a concrete fix can be provided
4. If a comment POST fails, skip it and continue
5. After posting all comments, print a summary: how many succeeded, how many failed
