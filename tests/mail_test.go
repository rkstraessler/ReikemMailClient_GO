package reikemmail_test

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rkstraessler/ReikemMailClient_GO"
)

type upload struct {
	name, filename, contentType string
	content                     []byte
}

func parseMultipart(t *testing.T, call request) ([]byte, []upload) {
	t.Helper()
	media, params, err := mime.ParseMediaType(call.header.Get("Content-Type"))
	if err != nil || media != "multipart/form-data" {
		t.Fatalf("want multipart, got %s (%v)", media, err)
	}
	reader := multipart.NewReader(bytes.NewReader(call.body), params["boundary"])
	var payload []byte
	var files []upload
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			return payload, files
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(part)
		if err != nil {
			t.Fatal(err)
		}
		if part.FormName() == "payload" && payload == nil && len(files) == 0 {
			payload = data
			continue
		}
		files = append(files, upload{part.FormName(), part.FileName(), part.Header.Get("Content-Type"), data})
	}
}

func TestSendMailMultipart(t *testing.T) {
	api, calls := captureAPI(t, http.StatusAccepted, nil, `{"status":202,"data":{"success":true,"queueIds":[45],
		"messageIds":["m-45"],"smtpMessageIds":["smtp-45"],"statuses":[{"queueId":45,"status":"queued"}],
		"filters":{"filtered":0,"rejections":[]},"replayed":false}}`)
	key := "\u00a0avv:42:v3:initial\u00a0"
	content := []byte{0, 255, 37, 80, 68, 70, 13, 10}
	options := reikemmail.SendMailOptions{
		To: []reikemmail.MailAddress{
			{Email: " ALICE@Example.com ", Name: reikemmail.Some(" Alice ")},
			{Email: "bob@example.com", Name: reikemmail.Null[string]()},
			{Email: "alice@example.com", Name: reikemmail.Some("duplicate")},
		},
		CC:             []reikemmail.MailAddress{{Email: "ALICE@example.com"}},
		BCC:            []reikemmail.MailAddress{{Email: "private@example.com", Name: reikemmail.Some(" ")}},
		ReplyTo:        []reikemmail.MailAddress{{Email: "alice@example.com"}},
		Subject:        "  Auftrag  ",
		HTML:           "\n<p>Hallo</p>\r\n<p>Tschüss</p>\n",
		IdempotencyKey: &key,
		Attachments:    []reikemmail.Attachment{{Filename: `Vertrag "2026".pdf`, ContentType: "Application/PDF", Content: content}},
	}
	response, err := newClient(t, api.URL).SendMail(t.Context(), options)
	if err != nil || response.HTTPCode != 202 || response.Data == nil {
		t.Fatalf("unexpected send response: %+v, %v", response, err)
	}
	data := response.Data
	if len(data.QueueIDs) != 1 || data.QueueIDs[0] != "45" || data.SMTPMessageIDs[0] != "smtp-45" ||
		data.Statuses[0].Status != reikemmail.MailStatusQueued || data.Replayed == nil || *data.Replayed || data.Filters.Rejections == nil {
		t.Fatalf("unexpected send data: %+v", data)
	}
	payload, files := parseMultipart(t, received(t, calls, "/api/v2/send"))
	equalJSON(t, payload, `{
		"to":[{"email":"alice@example.com","name":"Alice"},{"email":"bob@example.com","name":null}],
		"cc":[],"bcc":[{"email":"private@example.com","name":null}],"replyTo":[{"email":"alice@example.com"}],
		"subject":"  Auftrag  ","html":"\n<p>Hallo</p>\r\n<p>Tschüss</p>\n","idempotencyKey":"\u00a0avv:42:v3:initial\u00a0",
		"attachments":[{"filename":"Vertrag \"2026\".pdf","contentType":"application/pdf","uploadIndex":0}]}`)
	if len(files) != 1 || files[0].name != "attachments[0]" || files[0].filename != "Vertrag %222026%22.pdf" ||
		files[0].contentType != "application/pdf" || !bytes.Equal(files[0].content, content) {
		t.Fatalf("incorrect binary upload: %+v", files)
	}
	if options.To[0].Email != " ALICE@Example.com " || options.Attachments[0].ContentType != "Application/PDF" {
		t.Fatal("request options were modified")
	}
}

func TestSendMailListJSON(t *testing.T) {
	api, calls := captureAPI(t, http.StatusConflict, nil, `{"status":409,"data":{"success":false,
		"code":"idempotency_conflict","error":"Schlüssel wurde bereits verwendet."}}`)
	client := newClient(t, api.URL)
	options := mail()
	options.To = []reikemmail.MailAddress{{Email: `"X@Y"@[IPv6:2001:db8::1]`}, {Email: "x+y@example.test"}}
	options.Subject = "  Betreff ä  "
	options.HTML = " \n<p>A\nB\r\nC</p>\r "
	text := ""
	options.Text = &text
	options.DelayUntil = reikemmail.Some(int64(253402300799))
	options.FilterContext = reikemmail.Some(" \t A\r\nB\rC \n")
	options.Attachments = []reikemmail.Attachment{}
	response, err := client.SendMailList(t.Context(), options)
	if err != nil || response.HTTPCode != 409 || response.Data == nil || response.Data.Code != reikemmail.IdempotencyConflictCode {
		t.Fatalf("expected 409 API value, got %+v, %v", response, err)
	}
	call := received(t, calls, "/api/v2/send")
	if call.header.Get("Content-Type") != "application/json" {
		t.Fatalf("wrong content type: %s", call.header.Get("Content-Type"))
	}
	equalJSON(t, call.body, `{"to":[{"email":"\"x@y\"@[ipv6:2001:db8::1]"},{"email":"x+y@example.test"}],"subject":"  Betreff ä  ",
		"html":" \n<p>A\nB\r\nC</p>\r ","text":"","attachments":[],"delayUntil":253402300799,"filterContext":"A\nB\nC"}`)
}

func TestSendBatchDefaults(t *testing.T) {
	body := `{"status":207,"data":{"success":false,"queueIds":["1"],"results":[
		{"index":0,"success":true,"status":200,"data":{"success":true,"replayed":true,"statuses":[{
		"status":"future-status","filterDetails":{"ai":{"retry":{"pending":true,"attempt":2,"delay_seconds":10,
		"http_status":429,"retry_after_seconds":"10","next_attempt_at":"2030-01-01"},"result":{"reason_code":"future"}}}}]}},
		{"index":1,"success":false,"status":409,"data":{"code":"idempotency_conflict"}}]}}`
	api, calls := captureAPI(t, http.StatusMultiStatus, nil, body)
	client := newClient(t, api.URL)
	third := mail()
	third.Attachments = []reikemmail.Attachment{}
	third.CC = []reikemmail.MailAddress{}
	third.FilterContext = reikemmail.Null[string]()
	third.DelayUntil = reikemmail.Null[int64]()
	response, err := client.SendPersonalizedMailBatch(t.Context(), reikemmail.SendPersonalizedMailBatchOptions{
		Messages:      []reikemmail.SendMailOptions{mail(), mail(), third},
		Attachments:   []reikemmail.Attachment{{Filename: "Grüße.txt", ContentType: "TEXT/PLAIN", Content: []byte{'A', 0, 'B'}}},
		FilterContext: reikemmail.Some(" default "),
		CC:            []reikemmail.MailAddress{{Email: "a@example.test"}, {Email: "c@example.test"}},
		BCC:           []reikemmail.MailAddress{{Email: "c@example.test"}, {Email: "b@example.test"}},
		ReplyTo:       []reikemmail.MailAddress{{Email: "a@example.test"}},
	})
	if err != nil || response.Data == nil || len(response.Data.Results) != 2 || response.HTTPCode != 207 {
		t.Fatalf("unexpected batch response: %+v, %v", response, err)
	}
	first := response.Data.Results[0]
	entry := first.Data.Statuses[0]
	if !first.Success || !*first.Data.Replayed || entry.Status != "future-status" || entry.Status.IsKnown() ||
		!reikemmail.MailStatusQueued.IsKnown() || len(reikemmail.KnownMailStatuses()) != 8 ||
		entry.FilterDetails.AI.Retry.DelaySeconds != "10" || entry.FilterDetails.AI.Result.ReasonCode != "future" ||
		response.Data.Results[1].Data.Code != reikemmail.IdempotencyConflictCode {
		t.Fatalf("incorrect batch data: %+v", response.Data)
	}
	payload, files := parseMultipart(t, received(t, calls, "/api/v2/send-batch"))
	message := `{"to":[{"email":"a@example.test"}],"cc":[{"email":"c@example.test"}],"bcc":[{"email":"b@example.test"}],
		"replyTo":[{"email":"a@example.test"}],"subject":"Subject","html":"<p>Hallo!</p>","filterContext":"default",
		"attachments":[{"filename":"Grüße.txt","contentType":"text/plain","uploadIndex":%d}]}`
	last := `{"to":[{"email":"a@example.test"}],"cc":[],"bcc":[{"email":"c@example.test"},{"email":"b@example.test"}],
		"replyTo":[{"email":"a@example.test"}],"subject":"Subject","html":"<p>Hallo!</p>","filterContext":null,"delayUntil":null,"attachments":[]}`
	equalJSON(t, payload, fmt.Sprintf(`{"messages":[`+message+`,`+message+`,`+last+`]}`, 0, 1))
	if len(files) != 2 {
		t.Fatalf("want 2 uploads, got %d", len(files))
	}
	for i, file := range files {
		if file.name != fmt.Sprintf("attachments[%d]", i) || file.filename != "Grüße.txt" || !bytes.Equal(file.content, []byte{'A', 0, 'B'}) {
			t.Fatalf("wrong upload: %+v", file)
		}
	}
}

func TestBatchJSONAndLimits(t *testing.T) {
	api, calls := captureAPI(t, http.StatusOK, nil, successResponse)
	client := newClient(t, api.URL)
	options := reikemmail.SendPersonalizedMailBatchOptions{Messages: make([]reikemmail.SendMailOptions, 100)}
	for i := range options.Messages {
		options.Messages[i] = mail()
	}
	if _, err := client.SendPersonalizedMailBatch(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	call := received(t, calls, "/api/v2/send-batch")
	if call.header.Get("Content-Type") != "application/json" || bytes.Count(call.body, []byte(`"subject"`)) != 100 {
		t.Fatal("expected JSON for all 100 batch messages")
	}
	options.Messages = append(options.Messages, mail())
	overrides := mail()
	overrides.CC = []reikemmail.MailAddress{}
	for _, invalid := range []reikemmail.SendPersonalizedMailBatchOptions{
		options, {}, {Messages: []reikemmail.SendMailOptions{overrides}, CC: []reikemmail.MailAddress{{Email: "bad"}}},
		{Messages: []reikemmail.SendMailOptions{overrides}, Attachments: make([]reikemmail.Attachment, 101)},
	} {
		if _, err := client.SendPersonalizedMailBatch(t.Context(), invalid); err == nil {
			t.Error("invalid batch was accepted")
		}
	}
	if len(calls) != 0 {
		t.Error("invalid batch options made an HTTP request")
	}
}

func TestMailValidation(t *testing.T) {
	api, calls := captureAPI(t, http.StatusOK, nil, successResponse)
	client := newClient(t, api.URL)
	key, empty, bad := " key ", "", "\xff"
	attachment := reikemmail.Attachment{Filename: "a.pdf", ContentType: "application/pdf"}
	for _, tc := range []struct {
		name   string
		change func(*reikemmail.SendMailOptions)
	}{
		{"missing To", func(o *reikemmail.SendMailOptions) { o.To = nil }},
		{"bad email", func(o *reikemmail.SendMailOptions) { o.To[0].Email = "a..b@example.test" }},
		{"Unicode email whitespace", func(o *reikemmail.SendMailOptions) { o.To[0].Email += "\u00a0" }},
		{"email control", func(o *reikemmail.SendMailOptions) { o.To[0].Email += "\n" }},
		{"name control", func(o *reikemmail.SendMailOptions) { o.To[0].Name = reikemmail.Some("A\x7fB") }},
		{"address limit", func(o *reikemmail.SendMailOptions) { o.CC = make([]reikemmail.MailAddress, 101) }},
		{"reply limit", func(o *reikemmail.SendMailOptions) { o.ReplyTo = make([]reikemmail.MailAddress, 11) }},
		{"long subject", func(o *reikemmail.SendMailOptions) { o.Subject = strings.Repeat("😀", 5001) }},
		{"blank subject", func(o *reikemmail.SendMailOptions) { o.Subject = " \ufeff " }},
		{"blank HTML", func(o *reikemmail.SendMailOptions) { o.HTML = "\n\t" }},
		{"invalid Unicode", func(o *reikemmail.SendMailOptions) { o.Text = &bad }},
		{"idempotency whitespace", func(o *reikemmail.SendMailOptions) { o.IdempotencyKey = &key }},
		{"empty idempotency key", func(o *reikemmail.SendMailOptions) { o.IdempotencyKey = &empty }},
		{"long context", func(o *reikemmail.SendMailOptions) { o.FilterContext = reikemmail.Some(strings.Repeat("😀", 1001)) }},
		{"context control", func(o *reikemmail.SendMailOptions) { o.FilterContext = reikemmail.Some("\x00context") }},
		{"out-of-range delay", func(o *reikemmail.SendMailOptions) { o.DelayUntil = reikemmail.Some(int64(253402300800)) }},
		{"zero delay", func(o *reikemmail.SendMailOptions) { o.DelayUntil = reikemmail.Some(int64(0)) }},
		{"too many files", func(o *reikemmail.SendMailOptions) { o.Attachments = make([]reikemmail.Attachment, 101) }},
		{"duplicate filename", func(o *reikemmail.SendMailOptions) { o.Attachments = []reikemmail.Attachment{attachment, attachment} }},
		{"path in filename", func(o *reikemmail.SendMailOptions) {
			o.Attachments = []reikemmail.Attachment{{Filename: "../a.txt", ContentType: "text/plain"}}
		}},
		{"media parameters", func(o *reikemmail.SendMailOptions) {
			o.Attachments = []reikemmail.Attachment{{Filename: "a.txt", ContentType: "text/plain; charset=utf-8"}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := mail()
			tc.change(&options)
			if _, err := client.SendMail(t.Context(), options); err == nil {
				t.Error("invalid mail was accepted")
			}
		})
	}
	if len(calls) != 0 {
		t.Error("invalid mail made an HTTP request")
	}
}

func TestAttachmentFilesAndLimits(t *testing.T) {
	api, calls := captureAPI(t, http.StatusOK, nil, successResponse)
	client := newClient(t, api.URL)
	dir := t.TempDir()
	path := filepath.Join(dir, "invoice.pdf")
	content := []byte{0, 255, 10}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	files, err := reikemmail.CreateAttachments(path)
	if err != nil || len(files) != 1 || files[0].Filename != "invoice.pdf" || files[0].ContentType != "application/pdf" || !bytes.Equal(files[0].Content, content) {
		t.Fatalf("unexpected attachment: %+v, %v", files, err)
	}
	if _, err := client.CreateAttachments(dir); err == nil {
		t.Error("attachment helper accepted a directory")
	}
	if _, err := client.CreateAttachments(path + ".missing"); err == nil {
		t.Error("attachment helper accepted a missing path")
	}
	filePaths := make([]string, 101)
	for i := range filePaths {
		filePaths[i] = path
	}
	if _, err := reikemmail.CreateAttachments(filePaths...); err == nil {
		t.Error("attachment helper accepted 101 paths")
	}
	options := mail()
	options.Attachments = []reikemmail.Attachment{{Filename: "legacy.pdf", ContentType: "application/pdf", FilePath: path}}
	if _, err := client.SendMail(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	_, uploads := parseMultipart(t, received(t, calls, "/api/v2/send"))
	if len(uploads) != 1 || uploads[0].filename != "legacy.pdf" || !bytes.Equal(uploads[0].content, content) {
		t.Fatalf("unexpected file-backed upload: %+v", uploads)
	}
	shared := make([]reikemmail.Attachment, 100)
	for i := range shared {
		shared[i] = reikemmail.Attachment{Filename: fmt.Sprintf("%d.txt", i), ContentType: "text/plain"}
	}
	_, err = client.SendPersonalizedMailBatch(t.Context(), reikemmail.SendPersonalizedMailBatchOptions{
		Messages: []reikemmail.SendMailOptions{mail(), mail()}, Attachments: shared,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, uploads = parseMultipart(t, received(t, calls, "/api/v2/send-batch"))
	if len(uploads) != 200 || uploads[199].name != "attachments[199]" || uploads[199].filename != "99.txt" || len(uploads[199].content) != 0 {
		t.Fatalf("want 200 uploads with an empty last part, got %d uploads", len(uploads))
	}
}
