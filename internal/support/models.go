package support

import "time"

// ReportRequest is the payload sent by clients when submitting diagnostic logs.
type ReportRequest struct {
	InstallID string `json:"install_id"`
	Platform  string `json:"platform"`
	Version   string `json:"version"`
	Message   string `json:"message"`
	Logs      string `json:"logs"`
}

// Message is an individual message in a ticket thread.
type Message struct {
	Staff bool   `json:"staff"`
	Text  string `json:"text"`
	TS    int64  `json:"ts"` // Unix milliseconds for frontend compatibility
	Logs  string `json:"logs,omitempty"`
}

// Report represents a customer support ticket with diagnostics and conversation.
type Report struct {
	ID        int64     `json:"id"`
	InstallID string    `json:"install_id"`
	From      string    `json:"from"` // Alias for InstallID for UI display
	Platform  string    `json:"platform"`
	Version   string    `json:"version"`
	Message   string    `json:"message"`
	Logs      string    `json:"logs"`
	Status    string    `json:"status"` // "open", "answered", "closed"
	Messages  []Message `json:"messages"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ReplyRequest is submitted by support staff.
type ReplyRequest struct {
	Text string `json:"text"`
}

// StatusRequest updates ticket status.
type StatusRequest struct {
	Status string `json:"status"`
}
