// Package prmaintenance runs the repository's pull request automation.
package prmaintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Config identifies the trusted workflow context and the scoped installation.
type Config struct {
	Repository string
	Ref        string
	AppToken   string
	AppSlug    string
}

var (
	appSlugPattern = regexp.MustCompile(`^[a-z0-9-]+$`)
	shaPattern     = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// Run updates open pull request branches and reconciles verified Dependabot updates.
func Run(ctx context.Context, config Config, cmd Command, output io.Writer) error {
	if config.Repository != repository || config.Ref != "refs/heads/main" {
		return errors.New("unexpected GitHub repository or source ref")
	}
	if config.AppToken == "" {
		fmt.Fprintln(output, "::notice::The maintenance app is not configured. Dependabot labeling is available.")
	} else {
		if !appSlugPattern.MatchString(config.AppSlug) {
			return errors.New("unrecognized maintenance app identity")
		}
		repos, err := cliJSON[struct {
			Repositories []struct {
				FullName string `json:"full_name"`
			}
		}](ctx, cmd, config.AppToken, "api", "installation/repositories")
		if err != nil {
			return err
		}
		if len(repos.Repositories) != 1 || repos.Repositories[0].FullName != repository {
			return errors.New("maintenance token has unexpected repository access")
		}
		fmt.Fprintf(output, "Confirmed the maintenance token is scoped to %s.\n", repository)
	}

	pulls, err := githubPages[pull](ctx, cmd, "pulls?state=open&per_page=100")
	if err != nil {
		return err
	}
	var failures []error
	for _, pr := range pulls {
		if !pr.local() || config.AppToken == "" && !pr.dependabot() {
			continue
		}
		if err := maintain(ctx, config, cmd, output, pr.Number); err != nil {
			fmt.Fprintf(output, "#%d: %v\n", pr.Number, err)
			if !hasStatus(err, 404, 405, 409, 422) {
				failures = append(failures, fmt.Errorf("PR #%d: %w", pr.Number, err))
			}
		}
	}
	return errors.Join(failures...)
}

func maintain(ctx context.Context, config Config, cmd Command, output io.Writer, number int) error {
	path := fmt.Sprintf("pulls/%d", number)
	pr, err := githubAPI[pull](ctx, cmd, "", path)
	if err != nil || !pr.local() {
		return err
	}
	base, err := branchTip(ctx, cmd, pr.Base.Ref)
	if err != nil {
		return err
	}
	if !shaPattern.MatchString(base) || !shaPattern.MatchString(pr.Head.SHA) {
		return errors.New("invalid branch commit in GitHub response")
	}
	if config.AppToken != "" && (pr.Mergeable == nil || *pr.Mergeable) {
		stale, err := behind(ctx, cmd, base, pr.Head.SHA)
		if err != nil {
			return err
		}
		if stale {
			_, err := githubAPI[json.RawMessage](ctx, cmd, config.AppToken, path+"/update-branch",
				"--method", "PUT", "-f", "expected_head_sha="+pr.Head.SHA)
			switch {
			case err == nil:
				fmt.Fprintf(output, "#%d: requested an update from %q.\n", number, pr.Base.Ref)
				return nil
			case hasStatus(err, 403, 404, 405, 409, 422):
				fmt.Fprintf(output, "#%d: GitHub declined the branch update. %v\n", number, err)
				return nil
			default:
				return err
			}
		}
	}
	if !pr.dependabot() {
		return nil
	}
	return maintainDependabot(ctx, config, cmd, output, pr, path, base)
}

func maintainDependabot(ctx context.Context, config Config, cmd Command, output io.Writer, pr pull, path, base string) error {
	commits, err := githubPages[commit](ctx, cmd, path+"/commits?per_page=100")
	if err != nil {
		return err
	}
	files, err := githubPages[file](ctx, cmd, path+"/files?per_page=100")
	if err != nil {
		return err
	}
	current, err := githubAPI[pull](ctx, cmd, "", path)
	if err != nil || !current.dependabot() || current.Head.SHA != pr.Head.SHA {
		return err
	}
	isBaseAncestor := func(parent string) (bool, error) {
		if !shaPattern.MatchString(parent) {
			return false, nil
		}
		if parent == base {
			return true, nil
		}
		diverged, err := behind(ctx, cmd, parent, base)
		return !diverged, err
	}
	wanted, eligible, err := classify(current, commits, files, config.AppSlug, isBaseAncestor)
	if err != nil {
		return err
	}
	if err := syncLabels(ctx, cmd, current, wanted); err != nil {
		return err
	}
	if config.AppToken == "" || !eligible || current.Mergeable == nil || !*current.Mergeable {
		return nil
	}

	passed, err := ciPassed(ctx, cmd, current.Head)
	if err != nil || !passed {
		return err
	}
	protected, err := protectionActive(ctx, cmd)
	if err != nil || !protected {
		return err
	}
	number := strconv.Itoa(pr.Number)
	review, err := cliJSON[reviewState](ctx, cmd, "", "pr", "view", number, "--repo", repository, "--json",
		"state,isDraft,headRefOid,baseRefName,mergeable,mergeStateStatus,reviewDecision")
	if err != nil {
		return err
	}
	latestBase, err := branchTip(ctx, cmd, "main")
	if err != nil || latestBase != base {
		return err
	}
	latest, err := githubAPI[pull](ctx, cmd, "", path)
	if err != nil || !latest.dependabot() || latest.Head.SHA != pr.Head.SHA || needsManualReview(latest) ||
		review.State != "OPEN" || review.IsDraft || review.HeadRefOid != pr.Head.SHA || review.BaseRefName != "main" ||
		review.Mergeable != "MERGEABLE" || slices.Contains([]string{"BEHIND", "DIRTY", "UNKNOWN"}, review.MergeStateStatus) ||
		review.ReviewDecision == "CHANGES_REQUESTED" {
		return err
	}
	if review.ReviewDecision != "APPROVED" {
		if _, err := githubAPI[json.RawMessage](ctx, cmd, config.AppToken, path+"/reviews",
			"--method", "POST", "-f", "commit_id="+pr.Head.SHA, "-f", "event=APPROVE"); err != nil {
			return err
		}
	}
	if _, err := githubAPI[json.RawMessage](ctx, cmd, config.AppToken, path+"/merge",
		"--method", "PUT", "-f", "sha="+pr.Head.SHA, "-f", "merge_method=squash"); err != nil {
		return err
	}
	fmt.Fprintf(output, "#%s: squash-merged the verified revision.\n", number)
	return nil
}

func syncLabels(ctx context.Context, cmd Command, pr pull, wanted map[string]bool) error {
	var add, remove []string
	current := make(map[string]bool, len(pr.Labels))
	for _, label := range pr.Labels {
		current[label.Name] = true
		if !wanted[label.Name] && slices.Contains([]string{"automerge", "manual-review", "patch", "minor", "major"}, label.Name) {
			remove = append(remove, label.Name)
		}
	}
	for label := range wanted {
		if !current[label] {
			add = append(add, label)
		}
	}
	if len(add) == 0 && len(remove) == 0 {
		return nil
	}
	args := []string{"pr", "edit", strconv.Itoa(pr.Number), "--repo", repository}
	if len(add) != 0 {
		slices.Sort(add)
		args = append(args, "--add-label", strings.Join(add, ","))
	}
	if len(remove) != 0 {
		slices.Sort(remove)
		args = append(args, "--remove-label", strings.Join(remove, ","))
	}
	_, err := cmd(ctx, "", args...)
	return err
}

func hasStatus(err error, statuses ...int) bool {
	var apiError *APIError
	return errors.As(err, &apiError) && slices.Contains(statuses, apiError.Status)
}
