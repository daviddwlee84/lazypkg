package domain

import "time"

// MaintenanceJob is one proven update target, potentially shared by several
// adapters. Its ID excludes changing candidate versions so queue refreshes can
// retain the user's attempted/skipped selections.
type MaintenanceJob struct {
	ID             string          `json:"id"`
	Category       string          `json:"category"` // update, refresh, repair, guidance, current, missing
	Title          string          `json:"title"`
	Representative string          `json:"representative"`
	ManagerIDs     []string        `json:"manager_ids"`
	Target         string          `json:"target"`
	Health         []ManagerHealth `json:"health"`
	ApplySupported bool            `json:"apply_supported"`
	Reason         string          `json:"reason,omitempty"`
}

type MaintenanceQueue struct {
	GeneratedAt time.Time        `json:"generated_at"`
	Jobs        []MaintenanceJob `json:"jobs"`
}
