package reikemmail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// DownloadFile retrieves a file or returns its non-2xx API error envelope.
// It validates the complete response before saving to a target path.
func (c *Client) DownloadFile(ctx context.Context, options DownloadFileOptions) (DownloadResult, error) {
	source := DownloadSource(strings.ToLower(trimWhitespace(string(options.Source))))
	if source != Inbound && source != Outbound {
		return DownloadResult{}, errors.New("source must be inbound or outbound")
	}
	fileID, err := positiveInteger(options.FileID, "fileId")
	if err != nil {
		return DownloadResult{}, err
	}
	if path := options.TargetPath; path != nil && (*path == "" || strings.ContainsRune(*path, '\x00')) {
		return DownloadResult{}, errors.New("invalid targetPath")
	}
	body, err := json.Marshal(struct {
		Source DownloadSource `json:"source"`
		FileID string         `json:"fileId"`
	}{Source: source, FileID: fileID})
	if err != nil {
		return DownloadResult{}, err
	}
	response, err := c.post(ctx, "/files/download", "application/json", body)
	if err != nil {
		return DownloadResult{}, err
	}
	if response.status < 200 || response.status >= 300 {
		decoded, err := decodeResponse[APIData](response.body, response.status)
		return DownloadResult{APIError: decoded}, err
	}
	contentType := response.header.Get("Content-Type")
	if jsonMediaType.MatchString(contentType) || looksLikeAPIEnvelope(response.body) {
		return DownloadResult{}, &ResponseError{HTTPCode: response.status, reason: "JSON instead of a file"}
	}
	file := &DownloadResponse{
		HTTPCode: response.status, FileName: downloadFilename(response.header.Get("Content-Disposition")),
		ContentType: contentType, Bytes: len(response.body),
	}
	if options.TargetPath == nil {
		file.Content = response.body
	} else {
		file.Path, err = saveDownload(ctx, *options.TargetPath, file.FileName, response.body)
		if err != nil {
			return DownloadResult{}, err
		}
	}
	return DownloadResult{File: file}, nil
}

var (
	jsonMediaType  = regexp.MustCompile(`(?i)(?:application/json|\+json)(?:;|$)`)
	filenameUTF8   = regexp.MustCompile(`(?i)filename\*=UTF-8''([^;]+)`)
	filenameQuoted = regexp.MustCompile(`(?i)filename="([^"]+)"`)
	filenamePlain  = regexp.MustCompile(`(?i)filename=([^;]+)`)
)

func looksLikeAPIEnvelope(body []byte) bool {
	candidate := bytes.TrimLeft(body, jsWhitespace)
	if len(candidate) == 0 || candidate[0] != '{' {
		return false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(candidate, &fields); err != nil {
		return false
	}
	_, valid := envelopeStatus(fields)
	return valid
}

func downloadFilename(disposition string) string {
	const fallback = "download"
	var filename string
	if match := filenameUTF8.FindStringSubmatch(disposition); match != nil {
		decoded, err := url.PathUnescape(trimWhitespace(match[1]))
		if err != nil {
			return fallback
		}
		filename = decoded
	} else {
		match := filenameQuoted.FindStringSubmatch(disposition)
		if match == nil {
			match = filenamePlain.FindStringSubmatch(disposition)
		}
		if match == nil {
			return fallback
		}
		filename = match[1]
		if utf8.ValidString(filename) {
			filename = strings.TrimPrefix(filename, "\ufeff")
		} else {
			var latin1 strings.Builder
			for i := 0; i < len(filename); i++ {
				latin1.WriteRune(rune(filename[i]))
			}
			filename = latin1.String()
		}
	}
	if !validHeaderText(filename) || strings.ContainsRune(filename, ':') {
		return fallback
	}
	filename = trimWhitespace(filename)
	filename = filename[strings.LastIndexAny(filename, `/\`)+1:]
	if filename == "." || !filepath.IsLocal(filename) {
		return fallback
	}
	return filename
}

func saveDownload(ctx context.Context, target, filename string, body []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	info, err := os.Stat(target)
	hasSeparator := strings.HasSuffix(target, "/") || strings.HasSuffix(target, `\`)
	if err == nil && info.IsDir() || err != nil && hasSeparator {
		target = filepath.Join(target, filename)
	}
	saveError := errors.New("cannot save the downloaded file completely")
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", saveError
	}
	file, err := os.CreateTemp(dir, ".reikemmail-*")
	if err != nil {
		return "", saveError
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(body)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return "", saveError
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := os.Rename(file.Name(), target); err != nil {
		return "", saveError
	}
	return target, nil
}
