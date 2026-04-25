package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/wangle201210/ai-review/internal/vcs"
)

type Client struct {
	apiURL    string
	apiToken  string
	projectID string
	mrIID     string
	http      *http.Client
}

type diffRefs struct {
	BaseSHA  string `json:"base_sha"`
	HeadSHA  string `json:"head_sha"`
	StartSHA string `json:"start_sha"`
}

type mrResponse struct {
	ID           int       `json:"id"`
	IID          int       `json:"iid"`
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	SourceBranch string    `json:"source_branch"`
	TargetBranch string    `json:"target_branch"`
	DiffRefs     *diffRefs `json:"diff_refs"`
	Author       struct {
		Name string `json:"name"`
	} `json:"author"`
}

func New(apiURL, apiToken, projectID, mrIID string) *Client {
	return &Client{
		apiURL:    apiURL,
		apiToken:  apiToken,
		projectID: projectID,
		mrIID:     mrIID,
		http:      &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) api(path string) string {
	return fmt.Sprintf("%s/api/v4/projects/%s/%s", c.apiURL, url.PathEscape(c.projectID), path)
}

func (c *Client) doRequest(ctx context.Context, method, url string, body interface{}) ([]byte, int, error) {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reqBody = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("PRIVATE-TOKEN", c.apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	return respBody, resp.StatusCode, nil
}

func (c *Client) GetReviewInfo(ctx context.Context) (*vcs.ReviewInfo, error) {
	u := c.api(fmt.Sprintf("merge_requests/%s", c.mrIID))
	body, status, err := c.doRequest(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("get MR info: %w", err)
	}
	if status != 200 {
		return nil, fmt.Errorf("get MR info: HTTP %d: %s", status, string(body))
	}

	var mr mrResponse
	if err := json.Unmarshal(body, &mr); err != nil {
		return nil, fmt.Errorf("parse MR response: %w", err)
	}

	info := &vcs.ReviewInfo{
		ID:           strconv.Itoa(mr.IID),
		Title:        mr.Title,
		Description:  mr.Description,
		AuthorName:   mr.Author.Name,
		SourceBranch: mr.SourceBranch,
		TargetBranch: mr.TargetBranch,
	}

	if mr.DiffRefs != nil {
		info.BaseSHA = mr.DiffRefs.BaseSHA
		info.HeadSHA = mr.DiffRefs.HeadSHA
		info.StartSHA = mr.DiffRefs.StartSHA
	}

	return info, nil
}

func (c *Client) PostGeneralComment(ctx context.Context, message string) error {
	payload := map[string]string{"body": message}
	u := c.api(fmt.Sprintf("merge_requests/%s/notes", c.mrIID))
	body, status, err := c.doRequest(ctx, http.MethodPost, u, payload)
	if err != nil {
		return fmt.Errorf("post general comment: %w", err)
	}
	if status != 201 {
		return fmt.Errorf("post general comment: HTTP %d: %s", status, string(body))
	}
	return nil
}

type mrChangesResponse struct {
	Changes []struct {
		OldPath string `json:"old_path"`
		NewPath string `json:"new_path"`
		Diff    string `json:"diff"`
	} `json:"changes"`
}

func (c *Client) GetDiff(ctx context.Context) (string, error) {
	u := c.api(fmt.Sprintf("merge_requests/%s/changes", c.mrIID))
	body, status, err := c.doRequest(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", fmt.Errorf("get MR changes: %w", err)
	}
	if status != 200 {
		return "", fmt.Errorf("get MR changes: HTTP %d: %s", status, string(body))
	}

	var resp mrChangesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("parse MR changes: %w", err)
	}

	var result strings.Builder
	for _, ch := range resp.Changes {
		if ch.Diff == "" {
			continue
		}
		result.WriteString(fmt.Sprintf("diff --git a/%s b/%s\n", ch.OldPath, ch.NewPath))
		result.WriteString(fmt.Sprintf("--- a/%s\n+++ b/%s\n", ch.OldPath, ch.NewPath))
		result.WriteString(ch.Diff)
		if !strings.HasSuffix(ch.Diff, "\n") {
			result.WriteString("\n")
		}
	}

	return result.String(), nil
}
