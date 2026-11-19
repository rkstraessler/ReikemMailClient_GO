# REIKEM Mail Client for Go

[![CI](https://github.com/rkstraessler/ReikemMailClient_GO/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/rkstraessler/ReikemMailClient_GO/actions/workflows/ci.yml)

Go SDK for the REIKEM Mail API v2. Send email, submit personalized batches, query mail history, and download files. The module uses the Go standard library.

[GitHub Releases](https://github.com/rkstraessler/ReikemMailClient_GO/releases) · [Report an issue](https://github.com/rkstraessler/ReikemMailClient_GO/issues/new)

Also available for [TypeScript](https://github.com/rkstraessler/ReikemMailClient_TS) and [Rust](https://github.com/rkstraessler/ReikemMailClient_RS).

## Installation

Requires Go 1.27.4 or later. Install the tagged module from the public Go module proxy. [Package documentation on pkg.go.dev](https://pkg.go.dev/github.com/rkstraessler/ReikemMailClient_GO)

```sh
go get github.com/rkstraessler/ReikemMailClient_GO@v1.0.0
```

The Go package name is `reikemmail`. Use `@main` to try the latest development version.

## Quick start

Set `MAIL_API_URL`, `MAIL_AUTH_KEY`, and `MAIL_ADAPTER_KEY` in your environment. Update the recipient and idempotency key for your send request.

```go
package main

import (
	"context"
	"log"
	"os"

	"github.com/rkstraessler/ReikemMailClient_GO"
)

func main() {
	client, err := reikemmail.NewClient(reikemmail.Config{
		BaseURL:    os.Getenv("MAIL_API_URL"),
		AuthKey:    os.Getenv("MAIL_AUTH_KEY"),
		AdapterKey: os.Getenv("MAIL_ADAPTER_KEY"),
	})
	if err != nil {
		log.Fatal(err)
	}

	key := "contract:4711:v2:ready"
	response, err := client.SendMail(context.Background(), reikemmail.SendMailOptions{
		To:             []reikemmail.MailAddress{{Email: "customer@example.org", Name: reikemmail.Some("Alex Morgan")}},
		Subject:        "Your contract",
		HTML:           "<p>Hello, your contract is available.</p>",
		IdempotencyKey: &key,
	})
	if err != nil {
		log.Fatal(err)
	}
	if response.HTTPCode == 409 && response.Data != nil && response.Data.Code == reikemmail.IdempotencyConflictCode {
		log.Fatal("The idempotency key is already in use for a different send request")
	}
	if response.HTTPCode < 200 || response.HTTPCode >= 300 {
		log.Fatalf("Send request rejected (HTTP %d)", response.HTTPCode)
	}
	if response.Data != nil {
		log.Println("Queue IDs", response.Data.QueueIDs)
	}
}
```

### Configuration

- `BaseURL` accepts a URL origin, a reverse-proxy path, or an existing URL ending in `/api/send`, `/api/v2`, or `/api/v2/send`. With `https://mail.example.org/proxy`, the endpoint prefix is `/proxy/api/v2`.
- The client sends `AuthKey` as `Authorization: Bearer <authKey>` and `AdapterKey` as `X-Adapter-Key`. Supply both full keys using visible ASCII characters without spaces. Use HTTPS for live requests.
- `ConnectTimeout` covers DNS, TCP, and TLS and defaults to 10 seconds. `RequestTimeout` covers the full HTTP exchange, including the response body, and defaults to 30 seconds. An earlier context deadline takes precedence. The client is safe for concurrent use. It sends each HTTP request once and returns redirects to the caller.

## API overview

| Method | Endpoint | Use |
| --- | --- | --- |
| `SendMail` | `POST /api/v2/send` | Queue one message for one or more To recipients. |
| `SendMailList` | `POST /api/v2/send` | Alias for `SendMail` with the same options. |
| `SendPersonalizedMailBatch` | `POST /api/v2/send-batch` | Submit up to 100 messages with shared defaults and per-message results. |
| `GetMailHistory` | `POST /api/v2/history` | Filter by calendar dates and message IDs. |
| `DownloadFile` | `POST /api/v2/files/download` | Return file contents in memory or save them locally. |
| `CreateAttachments` | Local | Read files as binary attachments. Also available as a client method. |

### Send and batch options

Sending requires `To`, `Subject`, and `HTML`. Recipients are deduplicated in To, CC, BCC order. Reply-To is deduplicated separately. Use `Some(value)` to set a value or `Null[T]()` to send an explicit `null`. A nil slice omits the field, while an initialized empty slice sends `[]`. `DelayUntil` accepts Unix seconds, for example `Some(sendTime.Unix())`.

For the personalized batch example, place `contract.pdf` in the working directory. The first message inherits the batch attachment. The second overrides it with an empty slice. Shared recipient lists and filter context follow the same rules.

```go
package main

import (
	"context"
	"log"
	"os"

	"github.com/rkstraessler/ReikemMailClient_GO"
)

func main() {
	client, err := reikemmail.NewClient(reikemmail.Config{
		BaseURL:    os.Getenv("MAIL_API_URL"),
		AuthKey:    os.Getenv("MAIL_AUTH_KEY"),
		AdapterKey: os.Getenv("MAIL_ADAPTER_KEY"),
	})
	if err != nil {
		log.Fatal(err)
	}
	files, err := client.CreateAttachments("contract.pdf")
	if err != nil {
		log.Fatal(err)
	}

	response, err := client.SendPersonalizedMailBatch(context.Background(), reikemmail.SendPersonalizedMailBatchOptions{
		Attachments:   files,
		FilterContext: reikemmail.Some("Contract documents"),
		Messages: []reikemmail.SendMailOptions{
			{
				To:      []reikemmail.MailAddress{{Email: "customer@example.org"}},
				Subject: "Your contract",
				HTML:    "<p>Your contract is attached.</p>",
			},
			{
				To:          []reikemmail.MailAddress{{Email: "team@example.org"}},
				Subject:     "Internal update",
				HTML:        "<p>The documents have been sent.</p>",
				Attachments: []reikemmail.Attachment{},
			},
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Println("Batch HTTP status", response.HTTPCode)
	if response.Data != nil {
		for _, result := range response.Data.Results {
			log.Println("Message", result.Index, result.Status, result.Success)
		}
	}
}
```

`CreateAttachments` infers the MIME type from the file extension using Go's MIME mappings on the host. To specify an exact type, pass `Attachment{Filename: ..., ContentType: ..., FilePath: ...}`, or use `Content` for bytes you already have. The SDK encodes requests with attachments as multipart and other requests as JSON.

### History and downloads

`GetMailHistory` accepts calendar dates in `YYYY-MM-DD` format. `DateFrom` is required when you omit message IDs. `ParseMessageIDs` accepts comma-separated values. Pass IDs as decimal strings up to `9223372036854775807`.

```go
history, err := client.GetMailHistory(context.Background(), reikemmail.GetMailHistoryOptions{
	DateFrom:   "2026-11-01",
	DateTo:     "2026-11-30",
	MessageIDs: []string{"123", "9223372036854775807"},
})
if err != nil {
	log.Fatal(err)
}
if history.Data != nil && history.Data.History != nil {
	log.Println("Outbound messages", len(history.Data.History.Outbound))
}
```

`DownloadFile` takes `Inbound` or `Outbound` and a decimal `FileID`. Set `TargetPath` to save the file after the client validates the response. An existing directory or a path with a trailing separator uses the sanitized filename from the server. Leave `TargetPath` nil to read the bytes from `File.Content`.

```go
target := "downloads/"
result, err := client.DownloadFile(context.Background(), reikemmail.DownloadFileOptions{
	Source: reikemmail.Outbound, FileID: "123", TargetPath: &target,
})
if err != nil {
	log.Fatal(err)
}
if result.APIError != nil {
	log.Fatalf("Download rejected (HTTP %d)", result.APIError.HTTPCode)
}
log.Println("Saved", result.File.Path)
```

### Responses and errors

The client returns valid API envelopes as values, including HTTP 4xx and 5xx responses. `HTTPCode` holds the actual HTTP status, while `Status` comes from the JSON envelope. `APIInteger` preserves numeric and string IDs exactly as text. `Raw` preserves additional fields and exact number tokens, even when a valid envelope has a nil `Data` field in Go.

Invalid input, transport failures, unreadable responses, and file I/O failures return a Go error. Use `errors.As` to identify `*reikemmail.TransportError` and `*reikemmail.ResponseError`. Use `errors.Is` to check for `context.Canceled` and `context.DeadlineExceeded`.

## Development

```sh
go vet ./...
go test ./...
go test -race ./...
go build ./...
go mod tidy -diff
go mod verify
go run golang.org/x/vuln/cmd/govulncheck@v1.10.0 ./...
```

Tests use local `httptest` servers with test credentials. Run `go test -coverpkg=./ ./tests/...` to measure SDK coverage.

### GitHub automation

- **Branch protection** requires an up-to-date pull request, one current approval, resolved conversations, and the GitHub Actions `Required CI` check. GitHub uses squash merges and deletes merged branches. Repository administrators have a bypass for review and CI. A separate rule blocks force pushes and deletion for everyone. [Review and CI rules](.github/main-ruleset.json) · [History rules](.github/main-history-ruleset.json)
- **Dependabot** checks Go modules and GitHub Actions daily at 03:00 `Europe/Berlin` and groups routine compatible updates separately from security updates. [Dependabot configuration](.github/dependabot.yml)
- **PR maintenance** updates conflict-free branches in this repository for open pull requests targeting `main`. The `reikem-github-pr-maintenance` app can approve and squash-merge signed, compatible Dependabot updates after `Required CI` passes on the current head. Major, potentially breaking pre-1.0 minor, and unclassified updates need manual review. [PR maintenance workflow](.github/workflows/dependabot-automerge.yml)
- **autofix.ci** runs gofmt on pull requests. Its commits rerun CI. [Autofix workflow](.github/workflows/autofix.yml)

Install the official [autofix.ci app](https://github.com/apps/autofix-ci) and the repository-scoped PR maintenance app. Keep the app key in the indicated environment and restrict deployments to `main`. GitHub Actions uses read-only default permissions. Set the environment variable `PR_AUTOMATION_CLIENT_ID` and the encrypted secret `PR_AUTOMATION_PRIVATE_KEY` in `dependabot-maintenance`. To synchronize labels, run `bash .github/scripts/sync-labels.sh`.

## Releases

1. Create the approved SemVer tag at a commit on `main` with successful **Required CI**.
2. Run **Verify release** on `main` with the tag. The workflow checks the module path, race tests, build, dependency integrity, and vulnerability scan at the tagged commit.
3. The workflow downloads the tagged module from the public Go module proxy and verifies the version, source commit, and checksum.
4. Publish the GitHub Release notes in English and include the banner from the workflow summary. [Release workflow](.github/workflows/release.yml) · [Release banner](.github/assets/release-banners/reikem-mail-client-go-release-banner.png) · [Go module publishing](https://go.dev/doc/modules/publishing)

The Go module follows SemVer. A future Go module version `v2` would add the `/v2` import path suffix. This SDK already uses v2 of the Mail API.

## License

[MIT](LICENSE)
