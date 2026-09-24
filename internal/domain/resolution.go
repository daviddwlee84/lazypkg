package domain

// ConflictAssessment groups paths into installation instances. A matching
// command name alone never establishes that two installations are one project.
type ConflictAssessment struct {
	Name          string                 `json:"name"`
	Directory     string                 `json:"directory"`
	Scope         string                 `json:"scope"`
	Fingerprint   string                 `json:"fingerprint"`
	Installations []ConflictInstallation `json:"installations"`
	Issues        []Issue                `json:"issues,omitempty"`
}

type ConflictInstallation struct {
	ID                  string       `json:"id"`
	Package             Package      `json:"package"`
	Paths               []Executable `json:"paths"`
	Effective           bool         `json:"effective"`
	Project             string       `json:"project,omitempty"`
	Status              string       `json:"status"` // ready, blocked, guidance, unknown, runtime
	ManagerPath         string       `json:"manager_path,omitempty"`
	RuntimePath         string       `json:"runtime_path,omitempty"`
	Prefix              string       `json:"prefix,omitempty"`
	BinDir              string       `json:"bin_directory,omitempty"`
	Fingerprint         string       `json:"fingerprint"`
	ArtifactFingerprint string       `json:"artifact_fingerprint"`
	Commands            []string     `json:"commands,omitempty"`
	Dependents          []string     `json:"dependents,omitempty"`
	RequiredPaths       []string     `json:"required_paths,omitempty"`
	Blockers            []string     `json:"blockers,omitempty"`
	Warnings            []string     `json:"warnings,omitempty"`
	Evidence            []Evidence   `json:"evidence,omitempty"`
}

// ResolutionRequest deliberately selects one removal and one retained instance.
// IDs include provider context; an npm package ID cannot address two prefixes.
type ResolutionRequest struct {
	Name     string `json:"name"`
	KeepID   string `json:"keep_id"`
	RemoveID string `json:"remove_id"`
}

type ResolutionPlan struct {
	Request      ResolutionRequest    `json:"request"`
	Keep         ConflictInstallation `json:"keep"`
	Remove       ConflictInstallation `json:"remove"`
	Command      Command              `json:"command"`
	Fingerprint  string               `json:"fingerprint"`
	ExpectedPath string               `json:"expected_path,omitempty"`
}
