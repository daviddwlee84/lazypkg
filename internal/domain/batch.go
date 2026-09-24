package domain

import (
	"strings"
	"time"
)

type BatchUpgradeRequest struct {
	Targets []Package `json:"targets"`
	Source  string    `json:"source,omitempty"`
}

type BatchUpgradeEntry struct {
	ID               string      `json:"id"`
	Package          Package     `json:"package"`
	Targets          []Package   `json:"targets"`
	ObservedVersions []string    `json:"observed_versions,omitempty"`
	TargetVersion    string      `json:"target_version,omitempty"`
	State            string      `json:"state"` // planned, excluded, current
	Reason           string      `json:"reason,omitempty"`
	Plan             *ActionPlan `json:"plan,omitempty"`
	Context          string      `json:"context"`
	Fingerprint      string      `json:"fingerprint"`
}

type BatchUpgradePlan struct {
	Request     BatchUpgradeRequest `json:"request"`
	Entries     []BatchUpgradeEntry `json:"entries"`
	Context     string              `json:"context"`
	Fingerprint string              `json:"fingerprint"`
}

type BatchUpgradeItemResult struct {
	Entry   BatchUpgradeEntry `json:"entry"`
	State   string            `json:"state"` // pending, excluded, success, current, skipped, failed, unverified, drift, cancelled
	Message string            `json:"message,omitempty"`
	Result  ActionResult      `json:"result"`
}

type BatchUpgradeResult struct {
	Entries   []BatchUpgradeItemResult `json:"entries"`
	Paused    bool                     `json:"paused"`
	Message   string                   `json:"message"`
	Remaining []Package                `json:"remaining,omitempty"`
}

// BatchUpgradeTargetKey deliberately groups multiple mise versions as one tool
// install. It also prevents duplicate manager/package writes from hidden marks.
func BatchUpgradeTargetKey(p Package) string {
	return strings.Join([]string{p.Manager, p.Instance, NormalizePackageID(p.Manager, p.ID)}, "\x00")
}

// BatchUpgradeEligibility is shared by markings and execution planning. Native
// plans and fresh inventory remain mandatory even when a UI row is eligible.
func BatchUpgradeEligibility(p Package, m Manager, coverage []Coverage) (bool, string) {
	return BatchUpgradeEligibilityAt(p, m, coverage, time.Now())
}

// BatchUpgradeEligibilityAt freezes an actual observation time for preparation
// of one overview. UI callers and execution use the current-time wrapper.
func BatchUpgradeEligibilityAt(p Package, m Manager, coverage []Coverage, at time.Time) (bool, string) {
	if p.Manager == "" || p.ID == "" || m.ID != p.Manager {
		return false, "No exact manager/package selection"
	}
	if !m.Available {
		return false, "Manager unavailable; repair it first"
	}
	if !m.Supports("upgrade") {
		return false, "Manager does not support singular package upgrades"
	}
	if m.Scope != "" && m.Scope != "global" {
		return false, "Manager scope is not enabled for global/user upgrades"
	}
	if p.Scope != "" && p.Scope != "global" && !(p.Manager == "mise" && (p.Scope == "user runtime" || p.Scope == "runtime")) {
		return false, "Package scope is not enabled for this batch"
	}
	if NormalizePackageID("uvx", p.ID) == "meta-package-manager" {
		return false, "The active mpm backend must be changed through setup"
	}
	if p.Candidate {
		return false, "Select an installed package, not a search candidate"
	}
	if p.InventoryStale {
		return false, "Inventory is stale; refresh first"
	}
	fresh := false
	for _, c := range coverage {
		age := at.Sub(c.ObservedAt)
		if c.Manager == p.Manager && c.State == "complete" && !c.Stale && !c.ObservedAt.IsZero() && age >= 0 && age < time.Minute && (p.Instance == "" || c.Instance == "" || p.Instance == c.Instance) {
			fresh = true
		}
	}
	if !fresh {
		return false, "Fresh complete provider inventory is required"
	}
	if p.Manager == "mise" && p.LatestInstalled {
		return false, "The target runtime is already installed; activation is a separate action"
	}
	if p.Version == "" {
		return false, "The installed version is unknown"
	}
	if p.Manager == "gh-ext" {
		if p.Extension == nil {
			return false, "Registered extension metadata is required"
		}
		if p.Extension.Pinned || p.Extension.Kind == "local" || p.Extension.Kind == "unknown" {
			return false, "Pinned, local or unknown extensions are not batch upgrade targets"
		}
		if p.Extension.BlockedReason != "" {
			return false, p.Extension.BlockedReason
		}
		if p.Extension.Status != "available" && p.Extension.Status != "not-checked" {
			return false, "The extension has no verified available update"
		}
	}
	return true, ""
}
