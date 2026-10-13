package protocol

import (
	"time"
)

// TaskMessage defines the task parameters sent from Producer to Worker.
type TaskMessage struct {
	TaskID       string            `json:"task_id"`
	TenantID     string            `json:"tenant_id"`
	ActionTarget string            `json:"action_target"`
	SourceURI    string            `json:"source_uri"`
	CustomConfig map[string]string `json:"custom_config,omitempty"`
	Timestamp    int64             `json:"timestamp,omitempty"`
	Nonce        string            `json:"nonce,omitempty"`
}

// IsStale checks whether the task message timestamp is older than maxAge (if timestamp is set).
func (m *TaskMessage) IsStale(maxAge time.Duration) bool {
	if m.Timestamp == 0 {
		return false
	}
	msgTime := time.Unix(m.Timestamp, 0)
	return time.Since(msgTime) > maxAge
}

// ResultMessage defines the execution result returned by Worker to Supervisor / Producer.
type ResultMessage struct {
	TaskID        string `json:"task_id"`
	IsSuccess     bool   `json:"is_success"`
	ProcessedRows int64  `json:"processed_rows"`
	ErrorMessage  string `json:"error_message,omitempty"`
	ExecutionTime string `json:"execution_time"`
}

// UploadSlotResponse defines the direct pre-signed upload slot details for clients.
type UploadSlotResponse struct {
	TargetURL        string            `json:"target_url"`
	HTTPMethod       string            `json:"http_method"`
	RequiredHeaders  map[string]string `json:"required_headers,omitempty"`
	StorageReference string            `json:"storage_reference"`
	ExpiredAt        time.Time         `json:"expired_at"`
}

// IsExpired reports whether the upload slot has expired (PRD 7).
func (u *UploadSlotResponse) IsExpired() bool {
	return !u.ExpiredAt.IsZero() && time.Now().After(u.ExpiredAt)
}
