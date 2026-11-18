package reikemmail

import (
	"errors"
	"mime"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

var attachmentMediaType = regexp.MustCompile(`^[a-zA-Z0-9_!#$&^.+-]+/[a-zA-Z0-9_!#$&^.+-]+$`)

// CreateAttachments reads regular files into binary attachments. The MIME type
// is inferred from the extension, with application/octet-stream as fallback.
func CreateAttachments(paths ...string) ([]Attachment, error) {
	if len(paths) > 100 {
		return nil, errors.New("attachments must contain at most 100 entries")
	}
	files := make([]Attachment, 0, len(paths))
	for _, path := range paths {
		data, err := readAttachment(path)
		if err != nil {
			return nil, err
		}
		mediaType, _, _ := strings.Cut(mime.TypeByExtension(strings.ToLower(filepath.Ext(path))), ";")
		if mediaType == "" {
			mediaType = "application/octet-stream"
		}
		files = append(files, Attachment{Filename: filepath.Base(path), ContentType: mediaType, Content: data})
	}
	return normalizeAttachments(files)
}

// CreateAttachments reads attachments from paths using the package helper.
func (c *Client) CreateAttachments(paths ...string) ([]Attachment, error) {
	return CreateAttachments(paths...)
}

func normalizeAttachments(files []Attachment) ([]Attachment, error) {
	if files == nil {
		return nil, nil
	}
	if len(files) > 100 {
		return nil, errors.New("attachments must contain at most 100 entries")
	}
	result := make([]Attachment, 0, len(files))
	seen := make(map[string]bool, len(files))
	for _, file := range files {
		if file.FilePath != "" {
			if file.Content != nil {
				return nil, errors.New("attachment must supply either content or a file path")
			}
			data, err := readAttachment(file.FilePath)
			if err != nil {
				return nil, err
			}
			file.Content, file.FilePath = data, ""
		}
		name := trimWhitespace(file.Filename)
		if name == "" || name == "." || name == ".." || !validHeaderText(file.Filename) ||
			utf8.RuneCountInString(file.Filename) > 500 || strings.ContainsAny(file.Filename, `\/:`) {
			return nil, errors.New("invalid attachment filename")
		}
		if seen[file.Filename] {
			return nil, errors.New("attachment filenames must be unique per message")
		}
		seen[file.Filename] = true
		if !attachmentMediaType.MatchString(file.ContentType) {
			return nil, errors.New("invalid attachment contentType")
		}
		file.ContentType = strings.ToLower(file.ContentType)
		result = append(result, file)
	}
	return result, nil
}

func readAttachment(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("attachment must be a readable file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("attachment must be a readable file")
	}
	return data, nil
}
