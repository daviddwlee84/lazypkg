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
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Path         string   `json:"path,omitempty"`
	Version      string   `json:"version,omitempty"`
	Supported    bool     `json:"supported"`
	Available    bool     `json:"available"`
	Status       string   `json:"status"`
	Capabilities []string `json:"capabilities"`
	Errors       []string `json:"errors,omitempty"`
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
	Manager         string     `json:"manager"`
	ID              string     `json:"id"`
	Name            string     `json:"name,omitempty"`
	Version         string     `json:"version,omitempty"`
	Latest          string     `json:"latest,omitempty"`
	LatestInstalled bool       `json:"latest_installed,omitempty"`
	Description     string     `json:"description,omitempty"`
	Scope           string     `json:"scope"`
	Root            string     `json:"root,omitempty"`
	Commands        []string   `json:"commands,omitempty"`
	ExecutablePaths []string   `json:"executable_paths,omitempty"`
	Evidence        []Evidence `json:"evidence,omitempty"`
	Active          bool       `json:"active,omitempty"`
	Global          bool       `json:"global,omitempty"`
	ConfigSource    string     `json:"config_source,omitempty"`
}

func (p Package) Key() string {
	return strings.Join([]string{p.Manager, p.ID, p.Version, p.Scope, p.Root}, "\x00")
}

type Issue struct {
	Manager string `json:"manager,omitempty"`
	Message string `json:"message"`
}
type Snapshot struct {
	Packages   []Package `json:"packages"`
	Issues     []Issue   `json:"issues,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
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
	Kind     string        `json:"kind"`
	Title    string        `json:"title"`
	Request  ActionRequest `json:"request"`
	Steps    []Step        `json:"steps"`
	Warnings []string      `json:"warnings,omitempty"`
	Preview  string        `json:"preview,omitempty"`
	SetupIDs []string      `json:"setup_ids,omitempty"`
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
	Managers(context.Context) ([]Manager, error)
	Packages(context.Context, string, string, string) (Snapshot, error)
	Diagnose(context.Context, string) (DiagnosticReport, error)
	Plan(context.Context, ActionRequest) (ActionPlan, error)
	Execute(context.Context, ActionPlan, io.Reader, io.Writer, io.Writer) (ActionResult, error)
	SetupOptions(context.Context) ([]SetupOption, error)
	PlanSetup(context.Context, []string) (ActionPlan, error)
}
