package domain

import (
	"encoding/json"
	"time"
)

type PromptRequest struct {
	Recipe   string   `json:"recipe"`
	Target   string   `json:"target,omitempty"`
	Managers []string `json:"managers,omitempty"`
	Refresh  bool     `json:"refresh,omitempty"`
}

// Markdown is the sole clipboard/export payload. Context is the same versioned
// envelope for JSON consumers; rendering does not collect or launch anything.
type RenderedPrompt struct {
	Recipe         string          `json:"recipe"`
	ContextVersion int             `json:"context_version"`
	GeneratedAt    time.Time       `json:"generated_at"`
	Markdown       string          `json:"markdown"`
	Context        json.RawMessage `json:"context"`
	Warnings       []Issue         `json:"warnings,omitempty"`
}
