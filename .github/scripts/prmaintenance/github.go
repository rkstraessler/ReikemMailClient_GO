package prmaintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

const repository = "rkstraessler/ReikemMailClient_GO"
const requiredCheck = "Required CI"

// Command executes GitHub CLI commands using an optional installation token.
type Command func(ctx context.Context, token string, args ...string) ([]byte, error)

// APIError is an unsuccessful GitHub API request.
type APIError struct {
	Status int
}

// Error reports the HTTP status without including request data.
func (e *APIError) Error() string { return fmt.Sprintf("GitHub request failed (HTTP %d)", e.Status) }

type actor struct {
	ID    int
	Login string
	Type  string
}

func (a actor) dependabot() bool {
	return a.ID == 49699333 && a.Login == "dependabot[bot]" && a.Type == "Bot"
}

type gitIdentity struct {
	Email string
}

type commit struct {
	SHA       string
	Author    actor
	Committer actor
	Parents   []struct{ SHA string }
	Commit    struct {
		Message      string
		Author       gitIdentity
		Committer    gitIdentity
		Verification struct {
			Verified bool
			Reason   string
		}
	}
}

type file struct {
	Filename string
	Status   string
	Previous string `json:"previous_filename"`
}

type ref struct {
	Ref  string
	SHA  string
	Repo struct {
		FullName string `json:"full_name"`
	}
}

type pull struct {
	Number       int
	State        string
	Draft        bool
	Mergeable    *bool
	Title        string
	Body         string
	User         actor
	Base         ref
	Head         ref
	Labels       []struct{ Name string }
	Commits      int
	ChangedFiles int `json:"changed_files"`
}

func (p pull) local() bool {
	return p.State == "open" && p.Base.Repo.FullName == repository && p.Head.Repo.FullName == repository
}

func (p pull) dependabot() bool {
	return p.local() && p.User.dependabot() && !p.Draft && p.Base.Ref == "main" && strings.HasPrefix(p.Head.Ref, "dependabot/")
}

type reviewState struct {
	State            string
	IsDraft          bool
	HeadRefOid       string
	BaseRefName      string
	Mergeable        string
	MergeStateStatus string
	ReviewDecision   string
	AutoMergeRequest json.RawMessage
}

func cliJSON[T any](ctx context.Context, cmd Command, token string, args ...string) (T, error) {
	var value T
	raw, err := cmd(ctx, token, args...)
	if err != nil {
		return value, err
	}
	if len(bytes.TrimSpace(raw)) != 0 {
		err = json.Unmarshal(raw, &value)
	}
	return value, err
}

func githubAPI[T any](ctx context.Context, cmd Command, token, path string, args ...string) (T, error) {
	return cliJSON[T](ctx, cmd, token, append([]string{"api", "repos/" + repository + "/" + path}, args...)...)
}

func githubPages[T any](ctx context.Context, cmd Command, path string) ([]T, error) {
	pages, err := githubAPI[[][]T](ctx, cmd, "", path, "--paginate", "--slurp")
	var result []T
	for _, page := range pages {
		result = append(result, page...)
	}
	return result, err
}

func branchTip(ctx context.Context, cmd Command, branch string) (string, error) {
	parts := strings.Split(branch, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	result, err := githubAPI[struct{ Object struct{ SHA string } }](ctx, cmd, "", "git/ref/heads/"+strings.Join(parts, "/"))
	return result.Object.SHA, err
}

func behind(ctx context.Context, cmd Command, base, head string) (bool, error) {
	result, err := githubAPI[struct {
		BehindBy int `json:"behind_by"`
	}](ctx, cmd, "", "compare/"+base+"..."+head+"?per_page=1")
	return result.BehindBy > 0, err
}

func ciPassed(ctx context.Context, cmd Command, head ref) (bool, error) {
	runs, err := githubAPI[struct {
		Runs []struct {
			ID             int64
			Event          string
			Path           string
			HeadSHA        string `json:"head_sha"`
			HeadBranch     string `json:"head_branch"`
			HeadRepository struct {
				FullName string `json:"full_name"`
			} `json:"head_repository"`
			Status     string
			Conclusion string
		} `json:"workflow_runs"`
	}](ctx, cmd, "", "actions/workflows/ci.yml/runs?event=pull_request&head_sha="+head.SHA+"&per_page=1")
	if err != nil || len(runs.Runs) != 1 {
		return false, err
	}
	run := runs.Runs[0]
	if run.Event != "pull_request" || run.Path != ".github/workflows/ci.yml" ||
		run.HeadSHA != head.SHA || run.HeadBranch != head.Ref || run.HeadRepository.FullName != repository ||
		run.Status != "completed" || run.Conclusion != "success" {
		return false, nil
	}
	jobs, err := githubAPI[struct {
		Jobs []struct {
			Name       string
			Conclusion string
		}
	}](ctx, cmd, "", fmt.Sprintf("actions/runs/%d/jobs?filter=latest&per_page=100", run.ID))
	if err != nil {
		return false, err
	}
	count := 0
	for _, job := range jobs.Jobs {
		if job.Name == requiredCheck {
			if job.Conclusion != "success" {
				return false, nil
			}
			count++
		}
	}
	return count == 1, nil
}

func protectionActive(ctx context.Context, cmd Command) (bool, error) {
	rules, err := githubAPI[[]struct {
		Type       string
		Parameters struct {
			Strict   bool `json:"strict_required_status_checks_policy"`
			Reviews  int  `json:"required_approving_review_count"`
			Dismiss  bool `json:"dismiss_stale_reviews_on_push"`
			Required []struct {
				Context       string
				IntegrationID int `json:"integration_id"`
			} `json:"required_status_checks"`
		}
	}](ctx, cmd, "", "rules/branches/main")
	var checks, reviews bool
	for _, rule := range rules {
		switch rule.Type {
		case "required_status_checks":
			for _, check := range rule.Parameters.Required {
				checks = checks || (rule.Parameters.Strict && check.Context == requiredCheck && check.IntegrationID == 15368)
			}
		case "pull_request":
			reviews = reviews || rule.Parameters.Reviews >= 1 && rule.Parameters.Dismiss
		}
	}
	return checks && reviews, err
}
