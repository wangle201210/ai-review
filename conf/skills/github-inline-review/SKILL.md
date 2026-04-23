---
name: github-inline-review
description: Post inline review comments to GitHub PR via Review Comments API. Requires GITHUB_OWNER, GITHUB_REPO, GITHUB_PULL_NUMBER, GITHUB_SHA, GITHUB_TOKEN, GITHUB_API_URL environment variables.
---

# GitHub Pull Request Inline Review

Post inline comments to a GitHub PR via the Review Comments API using curl.

## Environment Variables

| Variable | Description |
|---|---|
| `GITHUB_OWNER` | Repository owner |
| `GITHUB_REPO` | Repository name |
| `GITHUB_PULL_NUMBER` | Pull Request number |
| `GITHUB_SHA` | Head commit SHA |
| `GITHUB_TOKEN` | GitHub token |
| `GITHUB_API_URL` | GitHub API base URL (e.g. `https://api.github.com`) |

## Post a single inline comment

```bash
curl -sf --request POST \
  --header "Authorization: Bearer $GITHUB_TOKEN" \
  --header "Accept: application/vnd.github.v3+json" \
  --header "Content-Type: application/json" \
  --data '{
    "body": "COMMENT_BODY",
    "path": "FILE_PATH",
    "line": LINE_NUMBER,
    "side": "RIGHT",
    "commit_id": "'"$GITHUB_SHA"'"
  }' \
  "$GITHUB_API_URL/repos/$GITHUB_OWNER/$GITHUB_REPO/pulls/$GITHUB_PULL_NUMBER/comments"
```

## Verify posted comments

```bash
curl -sf --header "Authorization: Bearer $GITHUB_TOKEN" \
  --header "Accept: application/vnd.github.v3+json" \
  "$GITHUB_API_URL/repos/$GITHUB_OWNER/$GITHUB_REPO/pulls/$GITHUB_PULL_NUMBER/comments"
```

## Rules

1. `path` must match the file path in the diff exactly
2. `line` is the line number in the new file; only comment on added lines
3. `commit_id` must be the head SHA of the PR
4. If a comment POST fails, skip it and continue
5. After posting all comments, print a summary
