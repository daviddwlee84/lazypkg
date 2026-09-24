package domain

// GHExtension describes one registered gh command. Version may be a release
// tag or abbreviated commit; FullVersion preserves the complete local identity.
// No raw Git remote credentials, authentication configuration or manifest are
// retained here.
type GHExtension struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Host          string `json:"host,omitempty"`
	Kind          string `json:"kind"` // binary, git, local, unknown
	Root          string `json:"root"`
	Path          string `json:"path"`
	Launcher      string `json:"launcher"`
	Version       string `json:"version,omitempty"`
	FullVersion   string `json:"full_version,omitempty"`
	Latest        string `json:"latest,omitempty"`
	Status        string `json:"status"` // not-checked, available, current, pinned, local, unknown, unsupported
	Reason        string `json:"reason,omitempty"`
	Fingerprint   string `json:"fingerprint"`
	Pinned        bool   `json:"pinned,omitempty"`
	BlockedReason string `json:"blocked_reason,omitempty"`
}

type GHExtensionPlan struct {
	Operation   string      `json:"operation"`
	Before      GHExtension `json:"before"`
	Fingerprint string      `json:"fingerprint"`
}
