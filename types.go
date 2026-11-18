// Package reikemmail provides a client for the REIKEM Mail API v2.
package reikemmail

import (
	"bytes"
	"encoding/json"
	"errors"
)

// Optional represents an omitted, explicit null, or populated JSON field.
// Its zero value is omitted when used in an SDK request.
type Optional[T any] struct {
	value *T
	set   bool
}

// Some marks a value as present, including a zero value such as an empty string.
func Some[T any](value T) Optional[T] { return Optional[T]{value: &value, set: true} }

// Null marks a field as present with a JSON null value.
func Null[T any]() Optional[T] { return Optional[T]{set: true} }

// Get returns the value and whether it is present and non-null.
func (o Optional[T]) Get() (T, bool) {
	if o.value == nil {
		var zero T
		return zero, false
	}
	return *o.value, true
}

// IsZero reports whether the field was omitted.
func (o Optional[T]) IsZero() bool { return !o.set }

// MarshalJSON encodes the value or null. Use the omitzero JSON tag to omit an unset field.
func (o Optional[T]) MarshalJSON() ([]byte, error) { return json.Marshal(o.value) }

// UnmarshalJSON records the supplied JSON value, including explicit null.
func (o *Optional[T]) UnmarshalJSON(raw []byte) error {
	var value *T
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	*o = Optional[T]{value: value, set: true}
	return nil
}

// MailAddress is an ASCII email address with an optional Unicode display name.
type MailAddress struct {
	Email string           `json:"email"`
	Name  Optional[string] `json:"name,omitzero"`
}

// Attachment supplies either in-memory bytes or a FilePath read when sending.
// Content must be nil when FilePath is set. Omit both for a zero-byte attachment.
type Attachment struct {
	Filename    string
	ContentType string
	Content     []byte
	FilePath    string
}

// SendMailOptions describes one message. Nil optional slices omit the fields.
// Non-nil empty slices send empty arrays. DelayUntil is in Unix seconds.
type SendMailOptions struct {
	To             []MailAddress
	CC             []MailAddress
	BCC            []MailAddress
	ReplyTo        []MailAddress
	Subject        string
	HTML           string
	Text           *string
	IdempotencyKey *string
	Attachments    []Attachment
	FilterContext  Optional[string]
	DelayUntil     Optional[int64]
}

// SendPersonalizedMailBatchOptions contains messages and shared defaults.
// Each present message field overrides the corresponding default in full.
type SendPersonalizedMailBatchOptions struct {
	Messages      []SendMailOptions
	Attachments   []Attachment
	FilterContext Optional[string]
	ReplyTo       []MailAddress
	CC            []MailAddress
	BCC           []MailAddress
}

// GetMailHistoryOptions filters history by calendar dates and/or decimal message IDs.
// Dates use YYYY-MM-DD. DateFrom is required when no IDs are supplied.
type GetMailHistoryOptions struct {
	DateFrom   string
	DateTo     string
	MessageIDs []string
}

// DownloadSource identifies the direction of a file's associated message.
type DownloadSource string

const (
	// Inbound selects a file from an incoming message.
	Inbound DownloadSource = "inbound"
	// Outbound selects a file from an outgoing message.
	Outbound DownloadSource = "outbound"
)

// DownloadFileOptions selects a file by its direction and decimal ID.
// A nil TargetPath returns bytes in memory. A directory path uses the server's
// safe filename. Other paths specify the exact output file.
type DownloadFileOptions struct {
	Source     DownloadSource
	FileID     string
	TargetPath *string
}

// DownloadResult holds either a file or a non-2xx JSON API response.
type DownloadResult struct {
	File     *DownloadResponse
	APIError *APIResponse[APIData]
}

// DownloadResponse contains file metadata and either bytes in memory or a saved path.
type DownloadResponse struct {
	HTTPCode    int
	FileName    string
	ContentType string // Empty if the server supplied no Content-Type header.
	Bytes       int
	Path        string
	Content     []byte
}

// APIResponse holds the server envelope and the actual HTTP status. Data is nil
// when the payload is an array or has a shape the typed model cannot represent.
// Raw preserves the complete original envelope and all JSON number tokens.
type APIResponse[T any] struct {
	Status   int64
	Data     *T
	HTTPCode int
	Error    json.RawMessage
	Code     string
	Raw      json.RawMessage
}

// SendResponse is returned by SendMail and SendMailList.
type SendResponse = APIResponse[SendData]

// BatchResponse is returned by SendPersonalizedMailBatch.
type BatchResponse = APIResponse[BatchData]

// APIInteger preserves a server number token or decimal string without rounding.
type APIInteger string

// UnmarshalJSON reads a JSON number or string into its lossless textual representation.
func (i *APIInteger) UnmarshalJSON(raw []byte) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) != 0 && (raw[0] == '-' || raw[0] >= '0' && raw[0] <= '9') && json.Valid(raw) {
		*i = APIInteger(raw)
		return nil
	}
	var value *string
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return errors.New("expected a JSON number or string")
	}
	*i = APIInteger(*value)
	return nil
}

// MailStatus is a queue status. The API can return values outside the known set.
type MailStatus string

const (
	// MailStatusFiltering indicates that the mail is being filtered.
	MailStatusFiltering MailStatus = "filtering"
	// MailStatusQueued indicates that the mail is queued.
	MailStatusQueued MailStatus = "queued"
	// MailStatusReview indicates that the mail awaits review.
	MailStatusReview MailStatus = "review"
	// MailStatusRejected indicates that filtering rejected the mail.
	MailStatusRejected MailStatus = "rejected"
	// MailStatusSent indicates that the mail was sent.
	MailStatusSent MailStatus = "sent"
	// MailStatusFailed indicates that delivery failed.
	MailStatusFailed MailStatus = "failed"
	// MailStatusPreparing indicates that delivery is being prepared.
	MailStatusPreparing MailStatus = "preparing"
	// MailStatusSending indicates that delivery is in progress.
	MailStatusSending MailStatus = "sending"
)

// KnownMailStatuses returns the statuses defined by the v2 client contract.
func KnownMailStatuses() []MailStatus {
	return []MailStatus{MailStatusFiltering, MailStatusQueued, MailStatusReview, MailStatusRejected,
		MailStatusSent, MailStatusFailed, MailStatusPreparing, MailStatusSending}
}

// IsKnown checks whether the status is defined by the v2 client contract.
func (s MailStatus) IsKnown() bool {
	for _, known := range KnownMailStatuses() {
		if s == known {
			return true
		}
	}
	return false
}

// IdempotencyConflictCode identifies reuse of a key with a different normalized message.
const IdempotencyConflictCode = "idempotency_conflict"

// Rejection describes a filter's decision for a rejected message.
type Rejection struct {
	Status MailStatus `json:"status,omitempty"`
	Code   string     `json:"code,omitempty"`
	Reason string     `json:"reason,omitempty"`
	Filter string     `json:"filter,omitempty"`
}

// Rejections maps filter rejection identifiers to details. The API can use an
// empty array in place of an empty map.
type Rejections map[string]Rejection

// UnmarshalJSON accepts a rejection map or the API's empty array.
func (r *Rejections) UnmarshalJSON(raw []byte) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("[]")) {
		*r = make(Rejections)
		return nil
	}
	var entries map[string]Rejection
	if err := json.Unmarshal(raw, &entries); err != nil {
		return err
	}
	*r = entries
	return nil
}

// FilterResults summarizes filtering and rejection reasons.
type FilterResults struct {
	Filtered   *int       `json:"filtered,omitempty"`
	Reasons    []string   `json:"reasons,omitzero"`
	Rejections Rejections `json:"rejections,omitzero"`
}

// AIRetry reports pending retries and their scheduling metadata.
type AIRetry struct {
	Pending           *bool       `json:"pending,omitempty"`
	Attempt           APIInteger  `json:"attempt,omitempty"`
	NextAttemptAt     *string     `json:"next_attempt_at,omitempty"`
	DelaySeconds      APIInteger  `json:"delay_seconds,omitempty"`
	HTTPStatus        *int        `json:"http_status,omitempty"`
	RetryAfterSeconds *APIInteger `json:"retry_after_seconds,omitempty"`
}

// AIResult describes the API's AI filter decision.
type AIResult struct {
	Decision         string   `json:"decision,omitempty"`
	ReasonCode       string   `json:"reason_code,omitempty"`
	Confidence       *float64 `json:"confidence,omitempty"`
	Reason           string   `json:"reason,omitempty"`
	Model            string   `json:"model,omitempty"`
	ModelDisplayName string   `json:"model_display_name,omitempty"`
	PromptVersion    string   `json:"prompt_version,omitempty"`
	PromptHash       string   `json:"prompt_hash,omitempty"`
}

// AIFilterDetails contains attempt, retry, and decision details for an AI filter.
type AIFilterDetails struct {
	Attempt     APIInteger `json:"attempt,omitempty"`
	LastErrorAt string     `json:"last_error_at,omitempty"`
	Retry       *AIRetry   `json:"retry,omitempty"`
	Result      *AIResult  `json:"result,omitempty"`
}

// FilterDetails carries processing details for a mail entry.
type FilterDetails struct {
	AI *AIFilterDetails `json:"ai,omitempty"`
}

// UnmarshalJSON accepts a details object or the API's empty array.
func (details *FilterDetails) UnmarshalJSON(raw []byte) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("[]")) {
		*details = FilterDetails{}
		return nil
	}
	type fields FilterDetails
	return json.Unmarshal(raw, (*fields)(details))
}

// MailEntry describes a message in a send status or history result.
type MailEntry struct {
	QueueID             APIInteger       `json:"queueId,omitempty"`
	MessageID           string           `json:"messageId,omitempty"`
	SMTPMessageID       string           `json:"smtpMessageId,omitempty"`
	Status              MailStatus       `json:"status,omitempty"`
	Index               *int             `json:"index,omitempty"`
	Success             *bool            `json:"success,omitempty"`
	Error               json.RawMessage  `json:"error,omitempty"`
	Errors              json.RawMessage  `json:"errors,omitempty"`
	Recipient           string           `json:"recipient,omitempty"`
	Subject             string           `json:"subject,omitempty"`
	HTML                string           `json:"html,omitempty"`
	Text                Optional[string] `json:"text,omitzero"`
	To                  []MailAddress    `json:"to,omitzero"`
	CC                  []MailAddress    `json:"cc,omitzero"`
	BCC                 []MailAddress    `json:"bcc,omitzero"`
	ReplyTo             []MailAddress    `json:"replyTo,omitzero"`
	IdempotencyKey      Optional[string] `json:"idempotencyKey,omitzero"`
	Regard              string           `json:"regard,omitempty"`
	Content             string           `json:"content,omitempty"`
	Filters             *FilterResults   `json:"filters,omitempty"`
	RejectionCode       *string          `json:"rejectionCode,omitempty"`
	RejectionReason     *string          `json:"rejectionReason,omitempty"`
	FilteredAt          *string          `json:"filteredAt,omitempty"`
	FilterNextAttemptAt *string          `json:"filterNextAttemptAt,omitempty"`
	FilterDetails       *FilterDetails   `json:"filterDetails,omitempty"`
}

// SendData holds dispatch IDs, status snapshots, filter details, or an API error.
type SendData struct {
	Success        *bool            `json:"success,omitempty"`
	Error          json.RawMessage  `json:"error,omitempty"`
	Code           string           `json:"code,omitempty"`
	QueueIDs       []APIInteger     `json:"queueIds,omitzero"`
	MessageIDs     []string         `json:"messageIds,omitzero"`
	SMTPMessageIDs []string         `json:"smtpMessageIds,omitzero"`
	Statuses       []MailEntry      `json:"statuses,omitzero"`
	Filters        *FilterResults   `json:"filters,omitempty"`
	IdempotencyKey Optional[string] `json:"idempotencyKey,omitzero"`
	Replayed       *bool            `json:"replayed,omitempty"`
}

// BatchResult contains the status and send data for one message in a batch.
type BatchResult struct {
	Index   int      `json:"index"`
	Success bool     `json:"success"`
	Status  int      `json:"status"`
	Data    SendData `json:"data"`
}

// BatchData contains the batch summary and individual message results.
type BatchData struct {
	SendData
	Results []BatchResult `json:"results,omitzero"`
}

// MailHistory groups entries by message direction.
type MailHistory struct {
	Outbound []MailEntry `json:"Outbound,omitzero"`
	Inbound  []MailEntry `json:"Inbound,omitzero"`
}

// APIData contains common response fields and optional history. Results may
// contain a batch results array or a filter results object.
type APIData struct {
	SendData
	Results json.RawMessage `json:"results,omitempty"`
	History *MailHistory    `json:"history,omitempty"`
}
