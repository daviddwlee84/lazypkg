// Package domain contains the contracts shared by the CLI, dashboard and providers.
package domain

import (
	"context"
	"io"
	"strings"
	"time"
)

const MPMVersion = "8.0.1"

type Manager struct {
	ComponentKind  string         `json:"component_kind,omitempty"`
	VersionSubject string         `json:"version_subject,omitempty"`
	Launcher       string         `json:"launcher,omitempty"`
	ReasonCode     string         `json:"reason_code,omitempty"`
	ID             string         `json:"id"`
	BackendID      string         `json:"backend_id,omitempty"`
	Name           string         `json:"name"`
	Path           string         `json:"path,omitempty"`
	Version        string         `json:"version,omitempty"`
	Supported      bool           `json:"supported"`
	Available      bool           `json:"available"`
	Status         string         `json:"status"`
	Capabilities   []string       `json:"capabilities"`
	Errors         []string       `json:"errors,omitempty"`
	Requirement    string         `json:"requirement,omitempty"`
	Reason         string         `json:"reason,omitempty"`
	Scope          string         `json:"scope,omitempty"`
	Groups         []string       `json:"groups,omitempty"`
	Maintained     bool           `json:"maintained"`
	SourceURL      string         `json:"source_url,omitempty"`
	Health         *ManagerHealth `json:"health,omitempty"`
}

func (m Manager) Supports(op string) bool {
	for _, v := range m.Capabilities {
		if v == op {
			return true
		}
	}
	return false
}

type Evidence struct {
	Kind   string `json:"kind"` // recorded, recognized, inferred, unknown
	Source string `json:"source"`
	Detail string `json:"detail"`
}
type Package struct {
	Manager           string       `json:"manager"`
	ID                string       `json:"id"`
	Name              string       `json:"name,omitempty"`
	Version           string       `json:"version,omitempty"`
	Latest            string       `json:"latest,omitempty"`
	LatestInstalled   bool         `json:"latest_installed,omitempty"`
	Description       string       `json:"description,omitempty"`
	Scope             string       `json:"scope"`
	Root              string       `json:"root,omitempty"`
	Commands          []string     `json:"commands,omitempty"`
	ExecutablePaths   []string     `json:"executable_paths,omitempty"`
	Evidence          []Evidence   `json:"evidence,omitempty"`
	Active            bool         `json:"active,omitempty"`
	Global            bool         `json:"global,omitempty"`
	ConfigSource      string       `json:"config_source,omitempty"`
	Instance          string       `json:"instance,omitempty"`
	Candidate         bool         `json:"candidate,omitempty"`
	InstallState      string       `json:"install_state,omitempty"`
	InstalledVersions []string     `json:"installed_versions,omitempty"`
	InventoryAt       time.Time    `json:"inventory_at,omitempty"`
	InventoryStale    bool         `json:"inventory_stale,omitempty"`
	PathMatches       []Executable `json:"path_matches,omitempty"`
}

func (p Package) Key() string {
	if p.Candidate {
		return strings.Join([]string{p.Manager, p.Instance, NormalizePackageID(p.Manager, p.ID)}, "\x00")
	}
	return strings.Join([]string{p.Manager, p.Instance, NormalizePackageID(p.Manager, p.ID), p.Version, p.Scope}, "\x00")
}

type Issue struct {
	Manager string `json:"manager,omitempty"`
	Message string `json:"message"`
	Kind    string `json:"kind,omitempty"`
}
type Coverage struct {
	Enrichment string    `json:"enrichment,omitempty"`
	Manager    string    `json:"manager"`
	Instance   string    `json:"instance,omitempty"`
	State      string    `json:"state"` // complete, failed, unavailable, unsupported, excluded, pending
	ObservedAt time.Time `json:"observed_at"`
	Stale      bool      `json:"stale,omitempty"`
	Message    string    `json:"message,omitempty"`
}
type PackageQuery struct {
	Kind           string   `json:"kind"`
	Query          string   `json:"query,omitempty"`
	Managers       []string `json:"managers,omitempty"`
	Group          string   `json:"group,omitempty"`
	Set            string   `json:"set,omitempty"`
	Refresh        bool     `json:"refresh,omitempty"`
	DeferInventory bool     `json:"defer_inventory,omitempty"`
}
type Snapshot struct {
	Packages          []Package  `json:"packages"`
	Issues            []Issue    `json:"issues,omitempty"`
	ObservedAt        time.Time  `json:"observed_at"`
	Coverage          []Coverage `json:"coverage,omitempty"`
	InventoryCoverage []Coverage `json:"inventory_coverage,omitempty"`
}
type Executable struct {
	Name         string     `json:"name"`
	Path         string     `json:"path"`
	Target       string     `json:"target,omitempty"`
	Chain        []string   `json:"chain,omitempty"`
	PathIndex    int        `json:"path_index"`
	Preferred    bool       `json:"preferred"`
	EquivalentTo string     `json:"equivalent_to,omitempty"`
	PackageKey   string     `json:"package_key,omitempty"`
	Manager      string     `json:"manager,omitempty"`
	Evidence     []Evidence `json:"evidence,omitempty"`
	Problem      string     `json:"problem,omitempty"`
}
type Finding struct {
	Kind    string   `json:"kind"`
	Name    string   `json:"name"`
	Message string   `json:"message"`
	Paths   []string `json:"paths,omitempty"`
}
type DiagnosticReport struct {
	Directory   string       `json:"directory"`
	Scope       string       `json:"scope"`
	Executables []Executable `json:"executables"`
	Findings    []Finding    `json:"findings"`
	Issues      []Issue      `json:"issues,omitempty"`
}
type Command struct {
	Path  string            `json:"path"`
	Args  []string          `json:"args,omitempty"`
	Dir   string            `json:"directory,omitempty"`
	Env   map[string]string `json:"env,omitempty"`
	Unset []string          `json:"unset,omitempty"`
}
type ActionRequest struct {
	Operation string `json:"operation"`
	Manager   string `json:"manager"`
	Package   string `json:"package"`
	Version   string `json:"version,omitempty"`
}
type Step struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Command     Command  `json:"command"`
	DependsOn   []string `json:"depends_on,omitempty"`
	URL         string   `json:"download_url,omitempty"`
	Digest      string   `json:"sha256,omitempty"`
	Destination string   `json:"destination,omitempty"`
	Verify      *Command `json:"verify,omitempty"`
	GuideURL    string   `json:"guide_url,omitempty"`
}
type ActionPlan struct {
	Resolution    *ResolutionPlan `json:"resolution,omitempty"`
	Kind          string          `json:"kind"`
	Title         string          `json:"title"`
	Request       ActionRequest   `json:"request"`
	Steps         []Step          `json:"steps"`
	Warnings      []string        `json:"warnings,omitempty"`
	Preview       string          `json:"preview,omitempty"`
	SetupIDs      []string        `json:"setup_ids,omitempty"`
	ManagerUpdate *ManagerHealth  `json:"manager_update,omitempty"`
}

type ManagerPreferences struct {
	Default    []string            `json:"default"`
	Order      []string            `json:"order"`
	Sets       map[string][]string `json:"sets"`
	Groups     map[string][]string `json:"groups"`
	DefaultSet string              `json:"default_set,omitempty"`
	Mouse      bool                `json:"mouse"`
}
type ManagerHealth struct {
	ComponentKind    string    `json:"component_kind,omitempty"`
	VersionSubject   string    `json:"version_subject,omitempty"`
	Launcher         string    `json:"launcher,omitempty"`
	ReasonCode       string    `json:"reason_code,omitempty"`
	Manager          string    `json:"manager"`
	Path             string    `json:"path"`
	Version          string    `json:"version"`
	Requirement      string    `json:"requirement,omitempty"`
	Compatible       bool      `json:"compatible"`
	Reason           string    `json:"reason,omitempty"`
	Owner            string    `json:"owner,omitempty"`
	OwnerPackage     string    `json:"owner_package,omitempty"`
	OwnerPath        string    `json:"owner_path,omitempty"`
	Runtime          string    `json:"runtime,omitempty"`
	RuntimePath      string    `json:"runtime_path,omitempty"`
	RuntimeVersion   string    `json:"runtime_version,omitempty"`
	Prefix           string    `json:"prefix,omitempty"`
	Channel          string    `json:"channel,omitempty"`
	Alternatives     []string  `json:"alternatives,omitempty"`
	UpdateStatus     string    `json:"update_status"`
	CandidateVersion string    `json:"candidate_version,omitempty"`
	Strategy         string    `json:"strategy,omitempty"`
	Recommendation   string    `json:"recommendation,omitempty"`
	GuideURL         string    `json:"guide_url,omitempty"`
	CheckedAt        time.Time `json:"checked_at"`
	Cached           bool      `json:"cached,omitempty"`
	Stale            bool      `json:"stale,omitempty"`
	ApplySupported   bool      `json:"apply_supported"`
	Fingerprint      string    `json:"fingerprint,omitempty"`
	ConfigPath       string    `json:"config_path,omitempty"`
}
type StepResult struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}
type ActionResult struct {
	Steps   []StepResult `json:"steps"`
	Message string       `json:"message"`
}
type SetupOption struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Installed   bool   `json:"installed"`
	Recommended bool   `json:"recommended"`
	GuideURL    string `json:"guide_url,omitempty"`
}

// Service is the single user-facing operation boundary. Execute never confirms;
// callers must present Plan and collect approval before passing it to Execute.
type Service interface {
	StreamQuery(context.Context, PackageQuery) <-chan QueryEvent
	AssessConflict(context.Context, string) (ConflictAssessment, error)
	PlanResolution(context.Context, ResolutionRequest) (ActionPlan, error)
	MaintenanceQueue(context.Context, []string, bool) (MaintenanceQueue, error)
	RenderPrompt(context.Context, PromptRequest) (RenderedPrompt, error)
	Managers(context.Context) ([]Manager, error)
	Query(context.Context, PackageQuery) (Snapshot, error)
	Preferences(context.Context) (ManagerPreferences, error)
	SaveManagerSet(context.Context, string, []string, bool) (ManagerPreferences, error)
	CheckManagers(context.Context, []string, bool) ([]ManagerHealth, error)
	PlanManagerUpdate(context.Context, string) (ActionPlan, error)
	Diagnose(context.Context, string) (DiagnosticReport, error)
	Plan(context.Context, ActionRequest) (ActionPlan, error)
	Execute(context.Context, ActionPlan, io.Reader, io.Writer, io.Writer) (ActionResult, error)
	SetupOptions(context.Context) ([]SetupOption, error)
	PlanSetup(context.Context, []string) (ActionPlan, error)
}
