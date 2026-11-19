package automation_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/rkstraessler/ReikemMailClient_GO/.github/scripts/prmaintenance"
)

const (
	repo       = "rkstraessler/ReikemMailClient_GO"
	appToken   = "fixture-app-token"
	appSlug    = "fixture-maintenance"
	pullNumber = 7
)

var oldBase, newBase, oldHead, newHead = strings.Repeat("1", 40), strings.Repeat("2", 40), strings.Repeat("3", 40), strings.Repeat("4", 40)

type response struct {
	body string
	err  error
}

type fixture struct {
	t       *testing.T
	replies map[string]response
	writes  []string
}

func (f *fixture) command(_ context.Context, token string, args ...string) ([]byte, error) {
	f.t.Helper()
	key := strings.Join(args, " ")
	key = strings.Replace(key, "api repos/"+repo+"/", "api ", 1)
	switch {
	case key == "api installation/repositories":
		if token != appToken {
			f.t.Fatal("installation lookup requires the scoped app token")
		}
	case strings.Contains(key, "/update-branch "), strings.Contains(key, "/reviews "), strings.Contains(key, "/merge "):
		if token != appToken {
			f.t.Fatal("a write used the wrong credential")
		}
		f.writes = append(f.writes, key)
	case strings.HasPrefix(key, "pr edit "):
		if token != "" {
			f.t.Fatal("labeling unexpectedly used the maintenance credential")
		}
		f.writes = append(f.writes, key)
	default:
		if token != "" {
			f.t.Fatal("a metadata read unexpectedly used the maintenance credential")
		}
	}
	value, ok := f.replies[key]
	if !ok {
		f.t.Fatalf("unexpected GitHub request %q", key)
	}
	return []byte(value.body), value.err
}

func (f *fixture) run(t *testing.T) error {
	t.Helper()
	return prmaintenance.Run(t.Context(), prmaintenance.Config{
		Repository: repo, Ref: "refs/heads/main", AppToken: appToken, AppSlug: appSlug,
	}, f.command, io.Discard)
}

func openPull(base, head, headRepo string) string {
	return fmt.Sprintf(`{"number":%d,"state":"open","mergeable":true,"base":{"ref":%q,"repo":{"full_name":%q}},
		"head":{"ref":"topic","sha":%q,"repo":{"full_name":%q}}}`, pullNumber, base, repo, head, headRepo)
}

func newFixture(t *testing.T, pull string) *fixture {
	return &fixture{t: t, replies: map[string]response{
		"api installation/repositories":                        {body: `{"repositories":[{"full_name":"` + repo + `"}]}`},
		"api pulls?state=open&per_page=100 --paginate --slurp": {body: "[[" + pull + "]]"},
		"api pulls/7": {body: pull},
	}}
}

func TestUpdatesWhenTheActualTargetBranchMovesAgain(t *testing.T) {
	update := func(head string) string {
		return "api pulls/7/update-branch --method PUT -f expected_head_sha=" + head
	}
	for _, tc := range []struct {
		name      string
		base      string
		head      string
		behind    string
		wantWrite bool
	}{
		{"initial stale branch", oldBase, oldHead, "1", true},
		{"up to date after update", oldBase, newHead, "0", false},
		{"target advances again", newBase, newHead, "1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, openPull("release/1", tc.head, repo))
			f.replies["api git/ref/heads/release/1"] = response{body: `{"object":{"sha":"` + tc.base + `"}}`}
			f.replies["api compare/"+tc.base+"..."+tc.head+"?per_page=1"] = response{body: `{"behind_by":` + tc.behind + "}"}
			f.replies[update(tc.head)] = response{body: `{"message":"Updating pull request branch."}`}
			if err := f.run(t); err != nil {
				t.Fatal(err)
			}
			var want []string
			if tc.wantWrite {
				want = []string{update(tc.head)}
			}
			if !slices.Equal(want, f.writes) {
				t.Fatalf("writes = %v, want %v", f.writes, want)
			}
		})
	}
}

func TestUpdatesRespectForkScopeAndMergeability(t *testing.T) {
	fork := newFixture(t, openPull("main", oldHead, "different-owner/fork"))
	delete(fork.replies, "api pulls/7")
	if err := fork.run(t); err != nil || len(fork.writes) != 0 {
		t.Fatalf("fork result: %v, writes: %v", err, fork.writes)
	}
	conflict := newFixture(t, strings.Replace(openPull("main", oldHead, repo), `"mergeable":true`, `"mergeable":false`, 1))
	conflict.replies["api git/ref/heads/main"] = response{body: `{"object":{"sha":"` + oldBase + `"}}`}
	if err := conflict.run(t); err != nil || len(conflict.writes) != 0 {
		t.Fatalf("conflict result: %v, writes: %v", err, conflict.writes)
	}
	declined := newFixture(t, openPull("main", oldHead, repo))
	declined.replies["api git/ref/heads/main"] = conflict.replies["api git/ref/heads/main"]
	declined.replies["api compare/"+oldBase+"..."+oldHead+"?per_page=1"] = response{body: `{"behind_by":1}`}
	declined.replies["api pulls/7/update-branch --method PUT -f expected_head_sha="+oldHead] = response{err: &prmaintenance.APIError{Status: 422}}
	if err := declined.run(t); err != nil || len(declined.writes) != 1 {
		t.Fatalf("declined result: %v, writes: %v", err, declined.writes)
	}
}

func TestRejectsAnInstallationTokenForMultipleRepositories(t *testing.T) {
	f := newFixture(t, openPull("main", oldHead, repo))
	f.replies["api installation/repositories"] = response{body: `{"repositories":[{"full_name":"` + repo + `"},{"full_name":"another/repo"}]}`}
	if err := f.run(t); err == nil || len(f.writes) != 0 {
		t.Fatalf("unexpected installation token scope accepted: %v", err)
	}
}

func TestDependabotAfterSignedBranchUpdate(t *testing.T) {
	for _, tc := range []struct {
		name       string
		next       string
		kind       string
		baseParent string
		checkState string
		approved   bool
	}{
		{"compatible change with successful CI", "1.2.4", "patch", newBase, "success", true},
		{"major change", "2.0.0", "major", newBase, "success", false},
		{"merge parent outside the target history", "1.2.4", "patch", oldBase, "success", false},
		{"required check failed", "1.2.4", "patch", newBase, "failure", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			branch := "dependabot/github_actions/actions-compatible"
			bot := `{"login":"dependabot[bot]","type":"Bot","id":49699333}`
			email := `{"email":"49699333+dependabot[bot]@users.noreply.github.com"}`
			app := `{"login":"` + appSlug + `[bot]","id":42,"type":"Bot"}`
			pull := fmt.Sprintf(`{"number":7,"state":"open","user":%s,"title":"ci: update actions","commits":2,
				"changed_files":1,"mergeable":true,"base":{"ref":"main","repo":{"full_name":%q}},
				"head":{"ref":%q,"sha":%q,"repo":{"full_name":%q}}}`, bot, repo, branch, newHead, repo)
			f := newFixture(t, pull)
			message, err := json.Marshal(fmt.Sprintf("Bumps owner/action from `1.2.3` to `%s`.\n\n---\nupdated-dependencies:\n"+
				"- dependency-name: owner/action\n  dependency-version: %s\n  update-type: version-update:semver-%s\n...", tc.next, tc.next, tc.kind))
			if err != nil {
				t.Fatal(err)
			}
			commits := fmt.Sprintf(`[[{"sha":%q,"author":%s,"committer":%s,"parents":[{"sha":%q}],
				"commit":{"verification":{"verified":true,"reason":"valid"},"author":%s,"committer":%s,"message":%s}},
				{"sha":%q,"author":%s,"committer":%s,"parents":[{"sha":%q},{"sha":%q}],
				"commit":{"verification":{"verified":true,"reason":"valid"}}}]]`,
				oldHead, bot, bot, oldBase, email, email, message, newHead, app, app, oldHead, tc.baseParent)
			f.replies["api git/ref/heads/main"] = response{body: `{"object":{"sha":"` + newBase + `"}}`}
			f.replies["api compare/"+newBase+"..."+newHead+"?per_page=1"] = response{body: `{"behind_by":0}`}
			f.replies["api compare/"+oldBase+"..."+newBase+"?per_page=1"] = response{body: `{"behind_by":1}`}
			f.replies["api pulls/7/commits?per_page=100 --paginate --slurp"] = response{body: commits}
			f.replies["api pulls/7/files?per_page=100 --paginate --slurp"] = response{body: `[[{"filename":".github/workflows/ci.yml","status":"modified"}]]`}
			for _, labels := range []string{"automerge,dependencies,github-actions,patch", "dependencies,github-actions,manual-review,patch", "dependencies,github-actions,major,manual-review"} {
				f.replies["pr edit 7 --repo "+repo+" --add-label "+labels] = response{}
			}
			f.replies["api actions/workflows/ci.yml/runs?event=pull_request&head_sha="+newHead+"&per_page=1"] = response{
				body: fmt.Sprintf(`{"workflow_runs":[{"id":14,"event":"pull_request","path":".github/workflows/ci.yml","head_sha":%q,
					"head_branch":%q,"head_repository":{"full_name":%q},"status":"completed","conclusion":"success"}]}`, newHead, branch, repo),
			}
			f.replies["api actions/runs/14/jobs?filter=latest&per_page=100"] = response{
				body: `{"jobs":[{"name":"Required CI","conclusion":"` + tc.checkState + `"}]}`,
			}
			f.replies["api rules/branches/main"] = response{body: `[
				{"type":"required_status_checks","parameters":{"strict_required_status_checks_policy":true,
					"required_status_checks":[{"context":"Required CI","integration_id":15368}]}},
				{"type":"pull_request","parameters":{"required_approving_review_count":1,"dismiss_stale_reviews_on_push":true}}]`}
			f.replies["pr view 7 --repo "+repo+" --json state,isDraft,headRefOid,baseRefName,mergeable,mergeStateStatus,reviewDecision"] = response{
				body: `{"state":"OPEN","headRefOid":"` + newHead + `","baseRefName":"main","mergeable":"MERGEABLE","reviewDecision":"REVIEW_REQUIRED"}`,
			}
			review := "api pulls/7/reviews --method POST -f commit_id=" + newHead + " -f event=APPROVE"
			merge := "api pulls/7/merge --method PUT -f sha=" + newHead + " -f merge_method=squash"
			f.replies[review] = response{body: `{"state":"APPROVED"}`}
			f.replies[merge] = response{}
			if err := f.run(t); err != nil {
				t.Fatal(err)
			}
			if slices.Contains(f.writes, review) != tc.approved || slices.Contains(f.writes, merge) != tc.approved {
				t.Fatalf("unexpected review/merge writes: %v", f.writes)
			}
		})
	}
}
