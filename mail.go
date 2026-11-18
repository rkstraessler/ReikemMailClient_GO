package reikemmail

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/netip"
	"net/textproto"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// MaxFilterContextCharacters is the API limit for Unicode code points in a filter context.
const MaxFilterContextCharacters = 1000

type mailPayload struct {
	To             []MailAddress    `json:"to"`
	CC             []MailAddress    `json:"cc,omitzero"`
	BCC            []MailAddress    `json:"bcc,omitzero"`
	ReplyTo        []MailAddress    `json:"replyTo,omitzero"`
	Subject        string           `json:"subject"`
	HTML           string           `json:"html"`
	Text           *string          `json:"text,omitempty"`
	IdempotencyKey *string          `json:"idempotencyKey,omitempty"`
	Attachments    []attachmentPart `json:"attachments,omitzero"`
	FilterContext  Optional[string] `json:"filterContext,omitzero"`
	DelayUntil     Optional[int64]  `json:"delayUntil,omitzero"`
}

type attachmentPart struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	UploadIndex int    `json:"uploadIndex"`
	content     []byte
}

func normalizeMail(options SendMailOptions) (mailPayload, error) {
	seen := make(map[string]bool)
	to, err := normalizeAddresses(options.To, "to", 100, seen)
	if err != nil {
		return mailPayload{}, err
	}
	if len(to) == 0 {
		return mailPayload{}, errors.New("to must not be empty")
	}
	if !validHeaderText(options.Subject) || trimWhitespace(options.Subject) == "" || utf8.RuneCountInString(options.Subject) > 5000 {
		return mailPayload{}, errors.New("invalid subject")
	}
	if !utf8.ValidString(options.HTML) || trimWhitespace(options.HTML) == "" {
		return mailPayload{}, errors.New("invalid html")
	}
	if key := options.IdempotencyKey; key != nil &&
		(*key == "" || !validHeaderText(*key) || phpTrim(*key) != *key || utf8.RuneCountInString(*key) > 255) {
		return mailPayload{}, errors.New("invalid idempotencyKey")
	}
	cc, err := normalizeAddresses(options.CC, "cc", 100, seen)
	if err != nil {
		return mailPayload{}, err
	}
	bcc, err := normalizeAddresses(options.BCC, "bcc", 100, seen)
	if err != nil {
		return mailPayload{}, err
	}
	replyTo, err := normalizeAddresses(options.ReplyTo, "replyTo", 10, make(map[string]bool))
	if err != nil {
		return mailPayload{}, err
	}
	if options.Text != nil && !utf8.ValidString(*options.Text) {
		return mailPayload{}, errors.New("invalid text")
	}
	filterContext, err := normalizeFilterContext(options.FilterContext)
	if err != nil {
		return mailPayload{}, err
	}
	if seconds, ok := options.DelayUntil.Get(); ok && (seconds < 1 || seconds > 253402300799) {
		return mailPayload{}, errors.New("delayUntil must be a positive integer at most 253402300799 (SQL year 9999)")
	}
	attachments, err := normalizeAttachments(options.Attachments)
	if err != nil {
		return mailPayload{}, err
	}
	var parts []attachmentPart
	if attachments != nil {
		parts = make([]attachmentPart, len(attachments))
		for i, file := range attachments {
			parts[i] = attachmentPart{Filename: file.Filename, ContentType: file.ContentType, content: file.Content}
		}
	}
	return mailPayload{
		To: to, CC: cc, BCC: bcc, ReplyTo: replyTo, Subject: options.Subject, HTML: options.HTML,
		Text: options.Text, IdempotencyKey: options.IdempotencyKey, Attachments: parts,
		FilterContext: filterContext, DelayUntil: options.DelayUntil,
	}, nil
}

func normalizeBatch(options SendPersonalizedMailBatchOptions) ([]mailPayload, error) {
	if len(options.Messages) == 0 || len(options.Messages) > 100 {
		return nil, errors.New("messages must contain between 1 and 100 entries")
	}
	attachments, err := normalizeAttachments(options.Attachments)
	if err != nil {
		return nil, err
	}
	filterContext, err := normalizeFilterContext(options.FilterContext)
	if err != nil {
		return nil, err
	}
	replyTo, err := normalizeAddresses(options.ReplyTo, "replyTo", 10, make(map[string]bool))
	if err != nil {
		return nil, err
	}
	cc, err := normalizeAddresses(options.CC, "cc", 100, make(map[string]bool))
	if err != nil {
		return nil, err
	}
	bcc, err := normalizeAddresses(options.BCC, "bcc", 100, make(map[string]bool))
	if err != nil {
		return nil, err
	}
	messages := make([]mailPayload, 0, len(options.Messages))
	for i, message := range options.Messages {
		if message.Attachments == nil {
			message.Attachments = attachments
		}
		if message.FilterContext.IsZero() {
			message.FilterContext = filterContext
		}
		if message.ReplyTo == nil {
			message.ReplyTo = replyTo
		}
		if message.CC == nil {
			message.CC = cc
		}
		if message.BCC == nil {
			message.BCC = bcc
		}
		payload, err := normalizeMail(message)
		if err != nil {
			return nil, fmt.Errorf("message %d: %w", i, err)
		}
		messages = append(messages, payload)
	}
	return messages, nil
}

func normalizeAddresses(addresses []MailAddress, field string, limit int, seen map[string]bool) ([]MailAddress, error) {
	if addresses == nil {
		return nil, nil
	}
	if len(addresses) > limit {
		return nil, fmt.Errorf("%s must contain at most %d entries", field, limit)
	}
	result := make([]MailAddress, 0, len(addresses))
	for _, address := range addresses {
		email := phpTrim(address.Email)
		if !validHeaderText(address.Email) || !validEmail(email) {
			return nil, fmt.Errorf("invalid %s email", field)
		}
		address.Email = strings.ToLower(email)
		if name, ok := address.Name.Get(); ok {
			if !validHeaderText(name) {
				return nil, fmt.Errorf("invalid %s name", field)
			}
			if name = phpTrim(name); name == "" {
				address.Name = Null[string]()
			} else {
				address.Name = Some(name)
			}
		}
		if !seen[address.Email] {
			seen[address.Email] = true
			result = append(result, address)
		}
	}
	return result, nil
}

var (
	emailAtom  = regexp.MustCompile(`^[a-zA-Z0-9!#$%&'*+/=?^_` + "`" + `{|}~-]+(?:\.[a-zA-Z0-9!#$%&'*+/=?^_` + "`" + `{|}~-]+)*$`)
	emailQuote = regexp.MustCompile(`^"(?:[\x20-\x21\x23-\x5B\x5D-\x7E]|\\[\x20-\x7E])*"$`)
	hostLabel  = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)
	ipZone     = regexp.MustCompile(`^[a-zA-Z0-9.:-]+$`)
)

func validEmail(email string) bool {
	if len(email) > 254 || strings.IndexFunc(email, func(r rune) bool { return r < ' ' || r > '~' }) >= 0 {
		return false
	}
	at := strings.LastIndexByte(email, '@')
	if at < 1 || at > 64 {
		return false
	}
	local, domain := email[:at], email[at+1:]
	if !emailAtom.MatchString(local) && !emailQuote.MatchString(local) {
		return false
	}
	if strings.HasPrefix(domain, "[") && strings.HasSuffix(domain, "]") {
		literal := strings.ToLower(domain[1 : len(domain)-1])
		address, err := netip.ParseAddr(strings.TrimPrefix(literal, "ipv6:"))
		return err == nil && (address.Zone() == "" || ipZone.MatchString(address.Zone()))
	}
	if !strings.Contains(domain, ".") {
		return false
	}
	for label := range strings.SplitSeq(domain, ".") {
		if !hostLabel.MatchString(label) {
			return false
		}
	}
	return true
}

func normalizeFilterContext(value Optional[string]) (Optional[string], error) {
	text, present := value.Get()
	if !present {
		return value, nil
	}
	if !utf8.ValidString(text) {
		return Optional[string]{}, errors.New("filterContext must be valid Unicode text or null")
	}
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	if strings.ContainsFunc(text, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) {
		return Optional[string]{}, errors.New("filterContext contains unsupported controls")
	}
	text = phpTrim(text)
	if utf8.RuneCountInString(text) > MaxFilterContextCharacters {
		return Optional[string]{}, errors.New("filterContext exceeds 1000 Unicode characters")
	}
	if text == "" {
		return Null[string](), nil
	}
	return Some(text), nil
}

func encodeMail(messages []mailPayload, batch bool) ([]byte, string, error) {
	var uploads []attachmentPart
	for i := range messages {
		for j := range messages[i].Attachments {
			messages[i].Attachments[j].UploadIndex = len(uploads)
			uploads = append(uploads, messages[i].Attachments[j])
		}
	}
	var payload any = messages[0]
	if batch {
		payload = struct {
			Messages []mailPayload `json:"messages"`
		}{Messages: messages}
	}
	encoded, err := json.Marshal(payload)
	if err != nil || len(uploads) == 0 {
		return encoded, "application/json", err
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("payload", string(encoded)); err != nil {
		return nil, "", err
	}
	for i, upload := range uploads {
		header := make(textproto.MIMEHeader)
		filename := strings.ReplaceAll(upload.Filename, `"`, "%22")
		header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="attachments[%d]"; filename="%s"`, i, filename))
		header.Set("Content-Type", upload.ContentType)
		part, err := writer.CreatePart(header)
		if err != nil {
			return nil, "", err
		}
		if _, err := part.Write(upload.content); err != nil {
			return nil, "", err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return body.Bytes(), writer.FormDataContentType(), nil
}

// jsWhitespace is the set removed by String.trim in the TypeScript client.
const jsWhitespace = "\t\n\v\f\r \u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000\ufeff"

func trimWhitespace(value string) string { return strings.Trim(value, jsWhitespace) }

func phpTrim(value string) string { return strings.Trim(value, " \t\n\r\x00\v") }

func validHeaderText(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsFunc(value, unicode.IsControl)
}

func positiveInteger(value, field string) (string, error) {
	digits := trimWhitespace(value)
	if digits == "" || strings.ContainsFunc(digits, func(r rune) bool { return r < '0' || r > '9' }) {
		return "", fmt.Errorf("invalid %s: expected a positive decimal integer", field)
	}
	value64, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || value64 <= 0 {
		return "", fmt.Errorf("invalid %s: outside positive signed 64-bit range", field)
	}
	return strconv.FormatInt(value64, 10), nil
}

func normalizeDate(value, field string) (string, error) {
	if value == "" {
		return "", nil
	}
	date := trimWhitespace(value)
	parsed, err := time.Parse(time.DateOnly, date)
	if err != nil || parsed.Year() == 0 || parsed.Format(time.DateOnly) != date {
		return "", fmt.Errorf("invalid %s: expected a calendar date in YYYY-MM-DD format", field)
	}
	return date, nil
}
