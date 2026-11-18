package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"

	"github.com/rkstraessler/ReikemMailClient_GO/.github/scripts/prmaintenance"
)

var statusPattern = regexp.MustCompile(`HTTP (\d{3})`)

func main() {
	config := prmaintenance.Config{
		Repository: os.Getenv("GITHUB_REPOSITORY"),
		Ref:        os.Getenv("GITHUB_REF"),
		AppToken:   os.Getenv("APP_TOKEN"),
		AppSlug:    os.Getenv("APP_SLUG"),
	}
	if err := prmaintenance.Run(context.Background(), config, runGitHubCLI, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runGitHubCLI(ctx context.Context, token string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1")
	if token != "" {
		cmd.Env = append(cmd.Env, "GH_TOKEN="+token)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err == nil {
		return output, nil
	}
	// Only report the status, since CLI diagnostics can include request data.
	if match := statusPattern.FindSubmatch(stderr.Bytes()); match != nil {
		status, _ := strconv.Atoi(string(match[1]))
		return nil, &prmaintenance.APIError{Status: status}
	}
	return nil, fmt.Errorf("GitHub CLI %s failed: %w", args[0], err)
}
