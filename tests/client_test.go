package reikemmail_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rkstraessler/ReikemMailClient_GO"
)

const successResponse = `{"status":200,"data":{"success":true}}`

type request struct {
	method, uri string
	header      http.Header
	length      int64
	body        []byte
}

func newClient(t *testing.T, baseURL string) *reikemmail.Client {
	t.Helper()
	return configuredClient(t, reikemmail.Config{BaseURL: baseURL, AuthKey: "!test-auth~", AdapterKey: "test-adapter"})
}

func configuredClient(t *testing.T, config reikemmail.Config) *reikemmail.Client {
	t.Helper()
	client, err := reikemmail.NewClient(config)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func captureAPI(t *testing.T, status int, headers http.Header, body string) (*httptest.Server, <-chan request) {
	t.Helper()
	calls := make(chan request, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
		}
		calls <- request{method: r.Method, uri: r.RequestURI, header: r.Header.Clone(), length: r.ContentLength, body: data}
		for name, values := range headers {
			w.Header()[name] = values
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return server, calls
}

func received(t *testing.T, calls <-chan request, path string) request {
	t.Helper()
	select {
	case call := <-calls:
		if call.method != http.MethodPost || call.uri != path {
			t.Fatalf("request = %s %s, want POST %s", call.method, call.uri, path)
		}
		if call.header.Get("Authorization") != "Bearer !test-auth~" || call.header.Get("X-Adapter-Key") != "test-adapter" {
			t.Fatal("missing or invalid authentication headers")
		}
		if call.length != int64(len(call.body)) {
			t.Fatalf("Content-Length = %d, read %d bytes", call.length, len(call.body))
		}
		return call
	default:
		t.Fatal("no request received")
		return request{}
	}
}

func mail() reikemmail.SendMailOptions {
	return reikemmail.SendMailOptions{
		To: []reikemmail.MailAddress{{Email: "a@example.test"}}, Subject: "Subject", HTML: "<p>Hallo!</p>",
	}
}

func equalJSON(t *testing.T, actual []byte, expected string) {
	t.Helper()
	var got, want any
	gotDecoder := json.NewDecoder(strings.NewReader(string(actual)))
	gotDecoder.UseNumber()
	wantDecoder := json.NewDecoder(strings.NewReader(expected))
	wantDecoder.UseNumber()
	if err := gotDecoder.Decode(&got); err != nil {
		t.Fatalf("could not decode request %q: %v", actual, err)
	}
	if err := wantDecoder.Decode(&want); err != nil {
		t.Fatalf("invalid test JSON: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("request JSON = %s\nwant %s", actual, expected)
	}
}

func TestClientURL(t *testing.T) {
	server, calls := captureAPI(t, http.StatusOK, nil, successResponse)
	for _, tc := range []struct{ base, prefix string }{
		{"", ""}, {"/", ""}, {"/proxy", "/proxy"}, {"/proxy/", "/proxy"},
		{"/proxy/api/send/", "/proxy"}, {"/proxy/api/v2/send", "/proxy"}, {"/proxy/api/v2/", "/proxy"},
		{"/proxy/old/%2e%2e/api/v2", "/proxy"}, {"/pr%2Foxy//edge/", "/pr%2Foxy//edge"},
		{"/proxy/api/send/api/v2/send", "/proxy/api/send"},
	} {
		t.Run(tc.base, func(t *testing.T) {
			client := newClient(t, server.URL+tc.base)
			if _, err := client.SendMail(t.Context(), mail()); err != nil {
				t.Fatal(err)
			}
			received(t, calls, tc.prefix+"/api/v2/send")
		})
	}
}

func TestInvalidConfig(t *testing.T) {
	for _, base := range []string{"relative", "ftp://example.test", "http:///proxy", "https://u:private@example.test",
		"https://example.test?q=private", "https://example.test/#frag", "https://example.test:65536"} {
		_, err := reikemmail.NewClient(reikemmail.Config{BaseURL: base, AuthKey: "key", AdapterKey: "adapter"})
		if err == nil || strings.Contains(err.Error(), "private") {
			t.Errorf("expected redacted URL error, got %v", err)
		}
	}
	for _, key := range []string{"", " bad", "bad key", "bad\nkey", "bad\x7f", "clé"} {
		for _, bad := range []reikemmail.Config{
			{BaseURL: "https://example.test", AuthKey: key, AdapterKey: "adapter"},
			{BaseURL: "https://example.test", AuthKey: "auth", AdapterKey: key},
		} {
			if _, err := reikemmail.NewClient(bad); err == nil {
				t.Errorf("accepted invalid auth key %q", key)
			}
		}
	}
	for _, timeout := range []time.Duration{-time.Second, 2147484 * time.Second} {
		for _, bad := range []reikemmail.Config{
			{BaseURL: "https://example.test", AuthKey: "auth", AdapterKey: "adapter", ConnectTimeout: timeout},
			{BaseURL: "https://example.test", AuthKey: "auth", AdapterKey: "adapter", RequestTimeout: timeout},
		} {
			if _, err := reikemmail.NewClient(bad); err == nil {
				t.Errorf("accepted timeout %s", timeout)
			}
		}
	}
}

func TestHistory(t *testing.T) {
	server, calls := captureAPI(t, http.StatusOK, nil, `{"status":2e2,"httpCode":999,"flag":true,"data":{
		"queueIds":[9007199254740993,"42",1e400],"extra":{"key":-9007199254740993},"results":{"filtered":1},
		"history":{"Outbound":[{"queueId":9223372036854775807,"messageId":"123","subject":"S","html":"H",
		"to":[{"email":"a@example.test","name":null}],"cc":[],"bcc":[],"replyTo":[],"idempotencyKey":"k",
		"status":"review","filterDetails":{"ai":{"retry":{"pending":true}}}}],"Inbound":[{"filterDetails":[]}]}}}`)
	client := newClient(t, server.URL+"/proxy")
	result, err := client.GetMailHistory(t.Context(), reikemmail.GetMailHistoryOptions{
		DateFrom: " 2024-02-29 ", DateTo: "2024-03-01", MessageIDs: []string{"9223372036854775807", "000123", "123"},
	})
	if err != nil {
		t.Fatal(err)
	}
	call := received(t, calls, "/proxy/api/v2/history")
	if call.header.Get("Content-Type") != "application/json" {
		t.Fatal("expected JSON content type")
	}
	equalJSON(t, call.body, `{"dateFrom":"2024-02-29","dateTo":"2024-03-01","messageIds":["9223372036854775807","123"]}`)
	if result.Status != 200 || result.HTTPCode != http.StatusOK || result.Data == nil {
		t.Fatalf("unexpected history envelope: %+v", result)
	}
	if !reflect.DeepEqual(result.Data.QueueIDs, []reikemmail.APIInteger{"9007199254740993", "42", "1e400"}) {
		t.Fatalf("unexpected queue IDs: %v", result.Data.QueueIDs)
	}
	entry := result.Data.History.Outbound[0]
	if entry.QueueID != "9223372036854775807" || entry.Subject != "S" || entry.Status != reikemmail.MailStatusReview ||
		entry.To[0].Name.IsZero() || entry.CC == nil || !*entry.FilterDetails.AI.Retry.Pending || result.Data.History.Inbound[0].FilterDetails == nil {
		t.Fatalf("unexpected history entry: %+v", entry)
	}
	if !strings.Contains(string(result.Raw), `"flag":true`) || !strings.Contains(string(result.Raw), `"key":-9007199254740993`) {
		t.Fatal("raw response lost an unknown field or precise number")
	}
	ids, err := reikemmail.ParseMessageIDs("123, , 00456, 123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetMailHistory(t.Context(), reikemmail.GetMailHistoryOptions{MessageIDs: ids}); err != nil {
		t.Fatal(err)
	}
	equalJSON(t, received(t, calls, "/proxy/api/v2/history").body, `{"messageIds":["123","456"]}`)
}

func TestInvalidHistory(t *testing.T) {
	server, calls := captureAPI(t, http.StatusOK, nil, successResponse)
	client := newClient(t, server.URL)
	for _, options := range []reikemmail.GetMailHistoryOptions{
		{}, {DateTo: "2026-01-01"}, {DateFrom: "2025-02-29"}, {DateFrom: "0000-01-01"},
		{DateFrom: "2026-01-01", DateTo: "2025-01-01"}, {DateFrom: " "},
		{MessageIDs: []string{"123abc"}}, {MessageIDs: []string{"9223372036854775808"}},
		{MessageIDs: []string{"0"}}, {MessageIDs: []string{"-1"}}, {MessageIDs: []string{"1e3"}},
	} {
		if _, err := client.GetMailHistory(t.Context(), options); err == nil {
			t.Errorf("accepted invalid history options %+v", options)
		}
	}
	if _, err := reikemmail.ParseMessageIDs("1, abc"); err == nil {
		t.Error("accepted invalid CSV")
	}
	if len(calls) != 0 {
		t.Error("invalid options made an HTTP request")
	}
}

func TestResponseErrors(t *testing.T) {
	for _, tc := range []struct{ body, reason string }{
		{`{"status":0,"data":{"x":01}}`, "invalid JSON"}, {"\xff", "invalid JSON"}, {"PRIVATE_RESPONSE", "invalid JSON"},
		{`"PRIVATE_RESPONSE"`, "invalid envelope"}, {`[]`, "invalid envelope"}, {`null`, "invalid envelope"},
		{`{"status":200}`, "invalid envelope"}, {`{"status":"200","data":{}}`, "invalid envelope"},
		{`{"Status":200,"data":{}}`, "invalid envelope"}, {`{"status":9007199254740992,"data":{}}`, "invalid envelope"},
		{`{"status":1.5,"data":{}}`, "invalid envelope"}, {`{"status":200,"data":null}`, "invalid envelope"},
	} {
		t.Run(tc.body, func(t *testing.T) {
			api, _ := captureAPI(t, http.StatusBadGateway, nil, tc.body)
			_, err := newClient(t, api.URL).SendMail(t.Context(), mail())
			var responseError *reikemmail.ResponseError
			if !errors.As(err, &responseError) || responseError.HTTPCode != 502 ||
				!strings.Contains(err.Error(), tc.reason) || strings.Contains(err.Error(), "PRIVATE_RESPONSE") {
				t.Fatalf("expected a sanitized response error, got %v", err)
			}
		})
	}
	for _, body := range []string{`{"status":500,"data":[],"code":"example","error":"detail"}`,
		`{"status":500,"data":{"queueIds":false},"code":"example","error":"detail"}`} {
		api, _ := captureAPI(t, http.StatusInternalServerError, nil, "\ufeff"+body)
		response, err := newClient(t, api.URL).SendMail(t.Context(), mail())
		if err != nil || response.Data != nil || response.Code != "example" || string(response.Error) != `"detail"` || string(response.Raw) != body {
			t.Fatalf("valid raw envelope = %+v, %v", response, err)
		}
	}
}

func TestNoRedirectOrRetry(t *testing.T) {
	t.Run("redirect", func(t *testing.T) {
		server, calls := captureAPI(t, http.StatusTemporaryRedirect, http.Header{"Location": {"%invalid"}}, `{"status":307,"data":{}}`)
		response, err := newClient(t, server.URL).SendMail(t.Context(), mail())
		if err != nil || response.HTTPCode != 307 {
			t.Fatalf("redirect should be returned as an API value: %+v, %v", response, err)
		}
		received(t, calls, "/api/v2/send")
		if len(calls) != 0 {
			t.Fatal("redirected or retried request")
		}
	})
	t.Run("truncated response", func(t *testing.T) {
		server, calls := captureAPI(t, http.StatusOK, http.Header{"Content-Length": {"1024"}}, "{")
		_, err := newClient(t, server.URL).SendMail(t.Context(), mail())
		var transportError *reikemmail.TransportError
		if !errors.As(err, &transportError) {
			t.Fatalf("expected transport error, got %v", err)
		}
		received(t, calls, "/api/v2/send")
		if len(calls) != 0 {
			t.Fatal("retried a truncated response")
		}
	})
}

func TestTransportCancellation(t *testing.T) {
	for _, tc := range []struct {
		name string
		want error
	}{
		{"response body timeout", context.DeadlineExceeded}, {"caller cancellation", context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ready := make(chan struct{}, 1)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				_, _ = io.WriteString(w, "{")
				w.(http.Flusher).Flush()
				ready <- struct{}{}
				<-r.Context().Done()
			}))
			defer server.Close()
			client := configuredClient(t, reikemmail.Config{
				BaseURL: server.URL, AuthKey: "auth", AdapterKey: "adapter", RequestTimeout: 700 * time.Millisecond,
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				_, err := client.SendMail(ctx, mail())
				result <- err
			}()
			select {
			case <-ready:
			case <-time.After(3 * time.Second):
				t.Fatal("server never began the streamed response")
			}
			if tc.want == context.Canceled {
				cancel()
			}
			err := <-result
			var transportError *reikemmail.TransportError
			if !errors.Is(err, tc.want) || !errors.As(err, &transportError) || calls.Load() != 1 {
				t.Fatalf("expected %v after one call, got %v after %d requests", tc.want, err, calls.Load())
			}
		})
	}
	t.Run("TLS handshake timeout", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		done := make(chan struct{})
		go func() {
			defer close(done)
			conn, err := listener.Accept()
			if err == nil {
				defer conn.Close()
				_, _ = io.Copy(io.Discard, conn)
			}
		}()
		client := configuredClient(t, reikemmail.Config{BaseURL: "https://" + listener.Addr().String(), AuthKey: "auth", AdapterKey: "adapter",
			ConnectTimeout: 100 * time.Millisecond, RequestTimeout: 3 * time.Second})
		started := time.Now()
		_, err = client.SendMail(t.Context(), mail())
		_ = listener.Close()
		<-done
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 2*time.Second {
			t.Fatalf("expected the connection deadline to cover TLS, got %v after %s", err, time.Since(started))
		}
	})
}

func TestConcurrentUse(t *testing.T) {
	const calls = 24
	var received atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		var data struct {
			IdempotencyKey string `json:"idempotencyKey"`
		}
		if err := json.NewDecoder(r.Body).Decode(&data); err != nil || data.IdempotencyKey == "" {
			http.Error(w, "invalid test request", http.StatusBadRequest)
			return
		}
		_, _ = fmt.Fprintf(w, `{"status":200,"data":{"idempotencyKey":%q}}`, data.IdempotencyKey)
	}))
	defer server.Close()
	client := newClient(t, server.URL)
	shared := mail()
	var wg sync.WaitGroup
	for i := range calls {
		wg.Go(func() {
			options := shared
			key := fmt.Sprintf("key:%d", i)
			options.IdempotencyKey = &key
			response, err := client.SendMail(t.Context(), options)
			if err != nil || response.Data == nil {
				t.Errorf("concurrent send: %+v, %v", response, err)
				return
			}
			if echoed, _ := response.Data.IdempotencyKey.Get(); echoed != key {
				t.Errorf("echoed %q, want %q", echoed, key)
			}
		})
	}
	wg.Wait()
	if received.Load() != calls {
		t.Errorf("received %d requests, want %d", received.Load(), calls)
	}
}
