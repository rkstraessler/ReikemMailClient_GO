package prmaintenance

import (
	"regexp"
	"strconv"
	"strings"
)

const botEmail = "49699333+dependabot[bot]@users.noreply.github.com"

var (
	actionFile   = regexp.MustCompile(`^\.github/workflows/[A-Za-z0-9_-]+\.ya?ml$`)
	securityRef  = regexp.MustCompile(`^dependabot/(go_modules|gomod|github_actions)/(go|actions)-security(-|/|$)`)
	breakingPR   = regexp.MustCompile(`^\w+(\([^\n]*\))?!:`)
	semver       = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	metadataItem = regexp.MustCompile(`(?m)^[ \t]*-[ \t]+dependency-name:[ \t]*([^\n]+)\n`)
	versionField = regexp.MustCompile(`(?m)^[ \t]+dependency-version:[ \t]*(.+)$`)
	kindField    = regexp.MustCompile(`(?m)^[ \t]+update-type:[ \t]*(.+)$`)
	grouped      = regexp.MustCompile("(?m)^Updates `([^`\\s]+)` from `?([^`\\s]+)`? to `?([^`\\s]+)`?[ \\t]*$")
	single       = regexp.MustCompile("(?m)^Bumps .+ from `?([^`\\s]+)`? to `?([^`\\s]+)`?\\.[ \\t]*$")
)

type versionChange struct {
	kind       string
	compatible bool
}

func classify(pr pull, commits []commit, files []file, appSlug string, isBaseAncestor func(string) (bool, error)) (map[string]bool, bool, error) {
	labels := map[string]bool{"dependencies": true}
	if securityRef.MatchString(pr.Head.Ref) {
		labels["security"] = true
	}
	verified := len(commits) == pr.Commits && pr.Commits > 0 && len(files) == pr.ChangedFiles && pr.ChangedFiles > 0
	for _, file := range files {
		var ecosystem string
		switch {
		case file.Filename == "go.mod" || file.Filename == "go.sum":
			ecosystem = "go"
		case actionFile.MatchString(file.Filename):
			ecosystem = "github-actions"
		}
		if ecosystem != "" {
			labels[ecosystem] = true
		}
		allowedStatus := file.Status == "modified" || file.Filename == "go.sum" && (file.Status == "added" || file.Status == "removed")
		verified = verified && ecosystem != "" && allowedStatus && file.Previous == ""
	}

	seen := make(map[string]bool, len(commits))
	var updates []versionChange
	for _, commit := range commits {
		verified = verified && shaPattern.MatchString(commit.SHA) && !seen[commit.SHA]
		seen[commit.SHA] = true
		if commit.Author.Login == "autofix-ci[bot]" {
			labels["autofix"] = true
		}
		changes, trusted, err := verifiedUpdates(commit, appSlug, isBaseAncestor)
		if err != nil {
			return nil, false, err
		}
		verified = verified && trusted
		updates = append(updates, changes...)
	}
	eligible := verified && seen[pr.Head.SHA] && len(updates) > 0 && !needsManualReview(pr)
	for _, update := range updates {
		if update.kind != "" {
			labels[update.kind] = true
		}
		eligible = eligible && update.compatible
	}
	if eligible {
		labels["automerge"] = true
	} else {
		labels["manual-review"] = true
	}
	return labels, eligible, nil
}

func needsManualReview(pr pull) bool {
	if breakingPR.MatchString(pr.Title) || strings.Contains(strings.ToLower(pr.Body), "maintainer changes") {
		return true
	}
	for _, label := range pr.Labels {
		if label.Name == "breaking-change" {
			return true
		}
	}
	return false
}

func verifiedUpdates(c commit, appSlug string, isBaseAncestor func(string) (bool, error)) ([]versionChange, bool, error) {
	info := c.Commit
	if !info.Verification.Verified || info.Verification.Reason != "valid" {
		return nil, false, nil
	}
	webFlow := c.Committer.Login == "web-flow" && c.Committer.ID == 19864447 && info.Committer.Email == "noreply@github.com"
	switch len(c.Parents) {
	case 1:
		botCommitter := c.Committer.dependabot() && info.Committer.Email == botEmail
		if c.Author.dependabot() && info.Author.Email == botEmail && (webFlow || botCommitter) {
			updates := commitUpdates(info.Message)
			return updates, len(updates) != 0, nil
		}
	case 2:
		appAuthor := appSlug != "" && c.Author.Login == appSlug+"[bot]" && c.Author.Type == "Bot"
		appCommitter := c.Committer.ID == c.Author.ID && c.Committer.Type == "Bot"
		if appAuthor && (webFlow || appCommitter) {
			trusted, err := isBaseAncestor(c.Parents[1].SHA)
			return nil, trusted, err
		}
	}
	return nil, false, nil
}

func commitUpdates(message string) []versionChange {
	message = strings.ReplaceAll(message, "\r\n", "\n")
	description, metadata, found := strings.Cut(message, "\n---\n")
	if !found || !strings.HasPrefix(metadata, "updated-dependencies:\n") || !strings.HasSuffix(strings.TrimSpace(metadata), "\n...") ||
		strings.Contains(metadata, "\n---\n") {
		return nil
	}
	records := metadataItem.FindAllStringSubmatchIndex(metadata, -1)
	changes := grouped.FindAllStringSubmatch(description, -1)
	if len(changes) == 0 {
		matches := single.FindAllStringSubmatch(description, -1)
		if len(records) != 1 || len(matches) != 1 {
			return nil
		}
		record := records[0]
		changes = [][]string{{"", metadataValue(metadata[record[2]:record[3]]), matches[0][1], matches[0][2]}}
	}
	if len(changes) != len(records) {
		return nil
	}
	var updates []versionChange
	for i, change := range changes {
		record := records[i]
		end := len(metadata)
		if i+1 < len(records) {
			end = records[i+1][0]
		}
		fields := metadata[record[1]:end]
		version, kind := versionField.FindAllStringSubmatch(fields, -1), kindField.FindAllStringSubmatch(fields, -1)
		if change[1] != metadataValue(metadata[record[2]:record[3]]) || len(version) != 1 || len(kind) != 1 ||
			metadataValue(version[0][1]) != change[3] {
			return nil
		}
		updates = append(updates, classifyVersion(change[2], change[3], metadataValue(kind[0][1])))
	}
	return updates
}

func metadataValue(value string) string { return strings.Trim(strings.TrimSpace(value), `"'`) }

func classifyVersion(previous, next, declared string) versionChange {
	parse := func(value string) ([3]int, bool) {
		var parts [3]int
		match := semver.FindStringSubmatch(value)
		if match == nil {
			return parts, false
		}
		for i := range parts {
			part, err := strconv.Atoi(match[i+1])
			if err != nil {
				return parts, false
			}
			parts[i] = part
		}
		return parts, true
	}
	old, validOld := parse(previous)
	current, validCurrent := parse(next)
	if !validOld || !validCurrent {
		return versionChange{}
	}
	for i, part := range old {
		if part == current[i] {
			continue
		}
		if part > current[i] {
			return versionChange{}
		}
		kind := [3]string{"major", "minor", "patch"}[i]
		compatible := i == 2 || i == 1 && old[0] > 0
		return versionChange{kind: kind, compatible: compatible && declared == "version-update:semver-"+kind}
	}
	return versionChange{}
}
