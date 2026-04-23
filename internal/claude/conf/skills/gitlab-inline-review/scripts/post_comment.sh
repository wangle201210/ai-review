#!/bin/bash
FILE_PATH="$1"
LINE_NUMBER="$2"
COMMENT_BODY="$3"

if [ -z "$FILE_PATH" ] || [ -z "$LINE_NUMBER" ] || [ -z "$COMMENT_BODY" ]; then
  echo "Usage: $0 <file_path> <line_number> <comment_body>"
  exit 1
fi

HTTP_CODE=$(curl -sf -o /dev/null -w "%{http_code}" --request POST \
  --header "PRIVATE-TOKEN: $GITLAB_TOKEN" \
  --header "Content-Type: application/json" \
  --data "$(jq -n \
    --arg body "$COMMENT_BODY" \
    --arg path "$FILE_PATH" \
    --arg line "$LINE_NUMBER" \
    --arg base "$GITLAB_BASE_SHA" \
    --arg head "$GITLAB_HEAD_SHA" \
    --arg start "$GITLAB_START_SHA" \
    '{
      body: $body,
      position: {
        base_sha: $base,
        head_sha: $head,
        start_sha: $start,
        position_type: "text",
        new_path: $path,
        old_path: $path,
        new_line: ($line | tonumber)
      }
    }'
  )" \
  "$GITLAB_URL/api/v4/projects/$GITLAB_PROJECT_ID/merge_requests/$GITLAB_MR_IID/discussions")

if [ "$HTTP_CODE" = "201" ]; then
  echo "OK: $FILE_PATH:$LINE_NUMBER"
  exit 0
else
  echo "FAIL ($HTTP_CODE): $FILE_PATH:$LINE_NUMBER"
  exit 1
fi
