package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/wangle201210/ai-review/internal/vcs"
)

type Client struct {
	apiURL     string
	apiToken   string
	owner      string
	repo       string
	pullNumber int
	http       *http.Client
	headSHA    string
}

func New(apiURL, apiToken, owner, repo string, pullNumber int) *Client {
	return &Client{
		apiURL:     apiURL,
		apiToken:   apiToken,
		owner:      owner,
		repo:       repo,
		pullNumber: pullNumber,
		http:       &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) api(path string) string {
	return fmt.Sprintf("%s/repos/%s/%s/%s", c.apiURL, c.owner, c.repo, path)
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
	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	return respBody, resp.StatusCode, nil
}

type prResponse struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Base  struct {
		SHA  string `json:"sha"`
		Ref  string `json:"ref"`
	} `json:"base"`
	Head struct {
		SHA  string `json:"sha"`
		Ref  string `json:"ref"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"head"`
}

func (c *Client) GetReviewInfo(ctx context.Context) (*vcs.ReviewInfo, error) {
	u := c.api(fmt.Sprintf("pulls/%d", c.pullNumber))
	body, status, err := c.doRequest(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("get PR info: %w", err)
	}
	if status != 200 {
		return nil, fmt.Errorf("get PR info: HTTP %d: %s", status, string(body))
	}

	var pr prResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		return nil, fmt.Errorf("parse PR response: %w", err)
	}

	c.headSHA = pr.Head.SHA

	return &vcs.ReviewInfo{
		ID:           strconv.Itoa(c.pullNumber),
		Title:        pr.Title,
		Description:  pr.Body,
		AuthorName:   pr.Head.User.Login,
		BaseSHA:      pr.Base.SHA,
		HeadSHA:      pr.Head.SHA,
		SourceBranch: pr.Head.Ref,
		TargetBranch: pr.Base.Ref,
	}, nil
}

func (c *Client) PostInlineComment(ctx context.Context, file string, line int, message string) error {
	if c.headSHA == "" {
		return fmt.Errorf("head SHA not available (call GetReviewInfo first)")
	}

	payload := map[string]interface{}{
		"body":      message,
		"path":      file,
		"line":      line,
		"side":      "RIGHT",
		"commit_id": c.headSHA,
	}
	u := c.api(fmt.Sprintf("pulls/%d/comments", c.pullNumber))
	body, status, err := c.doRequest(ctx, http.MethodPost, u, payload)
	if err != nil {
		return fmt.Errorf("post inline comment: %w", err)
	}
	if status != 201 {
		return fmt.Errorf("post inline comment: HTTP %d: %s", status, string(body))
	}
	return nil
}

func (c *Client) PostGeneralComment(ctx context.Context, message string) error {
	payload := map[string]string{"body": message}
	u := c.api(fmt.Sprintf("issues/%d/comments", c.pullNumber))
	body, status, err := c.doRequest(ctx, http.MethodPost, u, payload)
	if err != nil {
		return fmt.Errorf("post general comment: %w", err)
	}
	if status != 201 {
		return fmt.Errorf("post general comment: HTTP %d: %s", status, string(body))
	}
	return nil
}

func (c *Client) GetDiff(ctx context.Context) (string, error) {
	u := c.api(fmt.Sprintf("pulls/%d", c.pullNumber))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Accept", "application/vnd.github.v3.diff")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("get PR diff: HTTP %d", resp.StatusCode)
	}
	return string(data), nil
}
