package vcs

import "context"

type ReviewInfo struct {
	ID           string
	Title        string
	Description  string
	AuthorName   string
	BaseSHA      string
	HeadSHA      string
	StartSHA     string
	SourceBranch string
	TargetBranch string
}

type Client interface {
	GetReviewInfo(ctx context.Context) (*ReviewInfo, error)
	GetDiff(ctx context.Context) (string, error)
	PostInlineComment(ctx context.Context, file string, line int, message string) error
	PostGeneralComment(ctx context.Context, message string) error
}
