package reikemmail_test

import (
	"bytes"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rkstraessler/ReikemMailClient_GO"
)

func TestFileDownload(t *testing.T) {
	content := []byte{0, 255, 128, 10, 13, 42}
	api, calls := captureAPI(t, http.StatusOK, http.Header{
		"Content-Type": {"application/octet-stream"}, "Content-Disposition": {"attachment; filename*=UTF-8''Gr%C3%BC%C3%9Fe.pdf"},
	}, string(content))
	client := newClient(t, api.URL+"/proxy")
	options := reikemmail.DownloadFileOptions{Source: " OUTBOUND ", FileID: " 9223372036854775807 "}
	result, err := client.DownloadFile(t.Context(), options)
	if err != nil || result.File == nil || result.APIError != nil {
		t.Fatalf("download: %+v, %v", result, err)
	}
	file := result.File
	if file.FileName != "Grüße.pdf" || file.Bytes != len(content) || !bytes.Equal(file.Content, content) ||
		file.HTTPCode != 200 || file.ContentType != "application/octet-stream" || file.Path != "" {
		t.Fatalf("unexpected binary file: %+v", file)
	}
	call := received(t, calls, "/proxy/api/v2/files/download")
	equalJSON(t, call.body, `{"source":"outbound","fileId":"9223372036854775807"}`)
	if call.header.Get("Content-Type") != "application/json" {
		t.Fatal("expected JSON for download request")
	}
	dir := t.TempDir()
	exact := filepath.Join(dir, "exact.bin")
	if err := os.WriteFile(exact, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ target, expected string }{
		{dir, filepath.Join(dir, "Grüße.pdf")},
		{filepath.Join(dir, "missing") + string(filepath.Separator), filepath.Join(dir, "missing", "Grüße.pdf")},
		{exact, exact},
	} {
		options.TargetPath = &tc.target
		result, err := client.DownloadFile(t.Context(), options)
		if err != nil || result.File == nil || result.File.Path != tc.expected || result.File.Content != nil {
			t.Fatalf("save file: %+v, %v", result.File, err)
		}
		data, err := os.ReadFile(tc.expected)
		if err != nil || !bytes.Equal(data, content) {
			t.Fatalf("saved contents = %v, %v", data, err)
		}
	}
}

func TestDownloadFilename(t *testing.T) {
	for _, tc := range []struct{ header, name string }{
		{"", "download"}, {`attachment; filename="café.pdf"`, "café.pdf"},
		{"attachment; filename=\"caf\xe9.pdf\"", "café.pdf"}, {`attachment; filename=unquoted.pdf`, "unquoted.pdf"},
		{`attachment; filename="..\..\safe.pdf"`, "safe.pdf"}, {"attachment; filename*=UTF-8''..%2Fsafe.pdf", "safe.pdf"},
		{"attachment; filename*=UTF-8''%ZZ; filename=other", "download"},
		{"attachment; filename*=UTF-8''%FF", "download"}, {`attachment; filename=".."`, "download"},
		{"attachment; filename*=UTF-8''C%3Aevil.pdf", "download"}, {`attachment; filename="dir/"`, "download"},
	} {
		t.Run(tc.header, func(t *testing.T) {
			api, _ := captureAPI(t, http.StatusOK, http.Header{"Content-Disposition": {tc.header}}, "file")
			result, err := newClient(t, api.URL).DownloadFile(t.Context(), reikemmail.DownloadFileOptions{Source: reikemmail.Inbound, FileID: "1"})
			if err != nil || result.File == nil || result.File.FileName != tc.name {
				t.Fatalf("filename = %+v, %v, want %q", result.File, err, tc.name)
			}
		})
	}
}

func TestDownloadAPIErrorsPreserveTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keep.pdf")
	if err := os.WriteFile(path, []byte("keep-me"), 0o600); err != nil {
		t.Fatal(err)
	}
	options := reikemmail.DownloadFileOptions{Source: reikemmail.Inbound, FileID: "001", TargetPath: &path}
	api, calls := captureAPI(t, http.StatusNotFound, nil, `{"status":404,"data":{"success":false,"queueIds":[9223372036854775807]}}`)
	result, err := newClient(t, api.URL).DownloadFile(t.Context(), options)
	if err != nil || result.File != nil || result.APIError == nil || result.APIError.HTTPCode != 404 ||
		result.APIError.Data.QueueIDs[0] != "9223372036854775807" {
		t.Fatalf("expected JSON API error value, got %+v, %v", result, err)
	}
	equalJSON(t, received(t, calls, "/api/v2/files/download").body, `{"source":"inbound","fileId":"1"}`)
	for _, media := range []string{"application/json", "Application/Problem+JSON; charset=utf-8", "application/octet-stream"} {
		api, _ := captureAPI(t, http.StatusOK, http.Header{"Content-Type": {media}}, "\ufeff "+`{"status":422,"data":{"success":false}}`)
		_, err := newClient(t, api.URL).DownloadFile(t.Context(), options)
		var responseError *reikemmail.ResponseError
		if !errors.As(err, &responseError) || !strings.Contains(err.Error(), "JSON instead of a file") {
			t.Fatalf("expected JSON response error for %s, got %v", media, err)
		}
	}
	api, _ = captureAPI(t, http.StatusInternalServerError, nil, "PRIVATE_DOWNLOAD_RESPONSE")
	_, err = newClient(t, api.URL).DownloadFile(t.Context(), options)
	var responseError *reikemmail.ResponseError
	if !errors.As(err, &responseError) || responseError.HTTPCode != 500 || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatalf("expected sanitized JSON error, got %v", err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "keep-me" {
		t.Fatalf("original file changed: %q, %v", data, err)
	}
}

func TestDownloadValidation(t *testing.T) {
	api, calls := captureAPI(t, http.StatusOK, nil, "file")
	client := newClient(t, api.URL)
	empty, nul := "", "a\x00b"
	for _, invalid := range []reikemmail.DownloadFileOptions{
		{Source: "bad", FileID: "1"}, {Source: reikemmail.Inbound, FileID: "12junk"},
		{Source: reikemmail.Inbound, FileID: "9223372036854775808"},
		{Source: reikemmail.Inbound, FileID: "1", TargetPath: &empty}, {Source: reikemmail.Inbound, FileID: "1", TargetPath: &nul},
	} {
		if _, err := client.DownloadFile(t.Context(), invalid); err == nil {
			t.Error("invalid download options accepted")
		}
	}
	if len(calls) != 0 {
		t.Error("invalid download options made an HTTP request")
	}
}
