package reikemmail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Client sends requests to the REIKEM Mail API v2. Create it with NewClient.
// A client is safe for concurrent use. Callers should keep request input
// unchanged until the operation has returned.
type Client struct {
	endpointRoot   string
	authKey        string
	adapterKey     string
	requestTimeout time.Duration
	transport      *http.Transport
}

// SendMail submits one message, including when it has multiple To recipients.
// Valid API envelopes are returned for both successful and unsuccessful HTTP statuses.
func (c *Client) SendMail(ctx context.Context, options SendMailOptions) (*SendResponse, error) {
	message, err := normalizeMail(options)
	if err != nil {
		return nil, err
	}
	return postMail[SendData](ctx, c, "/send", []mailPayload{message}, false)
}

// SendMailList is an alias for SendMail with the same grouped-recipient semantics.
func (c *Client) SendMailList(ctx context.Context, options SendMailOptions) (*SendResponse, error) {
	return c.SendMail(ctx, options)
}

// SendPersonalizedMailBatch submits a batch of up to 100 personalized messages.
func (c *Client) SendPersonalizedMailBatch(ctx context.Context, options SendPersonalizedMailBatchOptions) (*BatchResponse, error) {
	messages, err := normalizeBatch(options)
	if err != nil {
		return nil, err
	}
	return postMail[BatchData](ctx, c, "/send-batch", messages, true)
}

// GetMailHistory requests mail history for the supplied dates and/or message IDs.
func (c *Client) GetMailHistory(ctx context.Context, options GetMailHistoryOptions) (*APIResponse[APIData], error) {
	payload, err := normalizeHistory(options)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return postAPI[APIData](ctx, c, "/history", "application/json", body)
}

func postMail[T any](ctx context.Context, c *Client, path string, messages []mailPayload, batch bool) (*APIResponse[T], error) {
	body, contentType, err := encodeMail(messages, batch)
	if err != nil {
		return nil, err
	}
	return postAPI[T](ctx, c, path, contentType, body)
}

func postAPI[T any](ctx context.Context, c *Client, path, contentType string, body []byte) (*APIResponse[T], error) {
	response, err := c.post(ctx, path, contentType, body)
	if err != nil {
		return nil, err
	}
	return decodeResponse[T](response.body, response.status)
}

type httpResult struct {
	body   []byte
	status int
	header http.Header
}

func (c *Client) post(ctx context.Context, path, contentType string, body []byte) (httpResult, error) {
	ctx, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpointRoot+path, bytes.NewReader(body))
	if err != nil {
		return httpResult{}, newTransportError(err)
	}
	req.Header.Set("Authorization", "Bearer "+c.authKey)
	req.Header.Set("X-Adapter-Key", c.adapterKey)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "")
	// A single round trip avoids redirect handling. The transport opens a new
	// connection for every request, which also rules out reused-connection retries.
	response, err := c.transport.RoundTrip(req)
	if err != nil {
		return httpResult{}, newTransportError(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return httpResult{}, newTransportError(err)
	}
	return httpResult{body: data, status: response.StatusCode, header: response.Header}, nil
}

func decodeResponse[T any](body []byte, httpCode int) (*APIResponse[T], error) {
	body = bytes.TrimPrefix(body, []byte("\xef\xbb\xbf"))
	var fields map[string]json.RawMessage
	if !utf8.Valid(body) {
		return nil, &ResponseError{HTTPCode: httpCode, reason: "invalid JSON"}
	}
	if err := json.Unmarshal(body, &fields); err != nil {
		var syntaxError *json.SyntaxError
		if errors.As(err, &syntaxError) {
			return nil, &ResponseError{HTTPCode: httpCode, reason: "invalid JSON"}
		}
		return nil, &ResponseError{HTTPCode: httpCode, reason: "an invalid envelope"}
	}
	status, valid := envelopeStatus(fields)
	if !valid {
		return nil, &ResponseError{HTTPCode: httpCode, reason: "an invalid envelope"}
	}
	result := &APIResponse[T]{Status: status, HTTPCode: httpCode, Error: fields["error"], Raw: body}
	_ = json.Unmarshal(fields["code"], &result.Code)
	if data := fields["data"]; data[0] == '{' {
		var typed T
		if json.Unmarshal(data, &typed) == nil {
			result.Data = &typed
		}
	}
	return result, nil
}

func envelopeStatus(fields map[string]json.RawMessage) (int64, bool) {
	if data := fields["data"]; len(data) == 0 || data[0] != '{' && data[0] != '[' {
		return 0, false
	}
	status, err := strconv.ParseFloat(string(fields["status"]), 64)
	const maxSafeInteger = 1<<53 - 1
	if err != nil || math.Abs(status) > maxSafeInteger || math.Trunc(status) != status {
		return 0, false
	}
	return int64(status), true
}

// ParseMessageIDs converts comma-separated message IDs to canonical decimal
// strings and removes duplicates. Empty CSV entries are ignored.
func ParseMessageIDs(csv string) ([]string, error) {
	ids := strings.Split(csv, ",")
	for i, id := range ids {
		ids[i] = trimWhitespace(id)
	}
	return normalizeMessageIDs(ids)
}

func normalizeMessageIDs(ids []string) ([]string, error) {
	var result []string
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		id, err := positiveInteger(id, "messageId")
		if err != nil {
			return nil, err
		}
		if !seen[id] {
			seen[id] = true
			result = append(result, id)
		}
	}
	return result, nil
}

type historyPayload struct {
	DateFrom   string   `json:"dateFrom,omitempty"`
	DateTo     string   `json:"dateTo,omitempty"`
	MessageIDs []string `json:"messageIds,omitempty"`
}

func normalizeHistory(options GetMailHistoryOptions) (historyPayload, error) {
	ids, err := normalizeMessageIDs(options.MessageIDs)
	if err != nil {
		return historyPayload{}, err
	}
	from, err := normalizeDate(options.DateFrom, "dateFrom")
	if err != nil {
		return historyPayload{}, err
	}
	to, err := normalizeDate(options.DateTo, "dateTo")
	if err != nil {
		return historyPayload{}, err
	}
	if from == "" && len(ids) == 0 {
		return historyPayload{}, errors.New("dateFrom is required when no messageIds are provided")
	}
	if to != "" && from > to {
		return historyPayload{}, errors.New("dateFrom must not be after dateTo")
	}
	return historyPayload{DateFrom: from, DateTo: to, MessageIDs: ids}, nil
}
