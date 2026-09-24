package maintenance

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/domain"
)

func (e *Engine) Queue(ctx context.Context, managers []domain.Manager, force bool) (domain.MaintenanceQueue, error) {
	h, err := e.Check(ctx, managers, force)
	return BuildQueue(h, e.now()), err
}

// BuildQueue groups only explicitly supported aliases whose observed ownership
// proves one update target. Sharing an executable basename is never enough.
func BuildQueue(health []domain.ManagerHealth, at time.Time) domain.MaintenanceQueue {
	q := domain.MaintenanceQueue{GeneratedAt: at.UTC(), Jobs: []domain.MaintenanceJob{}}
	groups := map[string][]domain.ManagerHealth{}
	for _, h := range health {
		if h.Manager == "" {
			continue
		}
		h.Alternatives = append([]string(nil), h.Alternatives...)
		key := provenTarget(h)
		if key == "" {
			key = "instance\x00" + h.Manager + "\x00" + filepath.Clean(h.Path)
		}
		groups[key] = append(groups[key], h)
	}
	for key, members := range groups {
		sort.Slice(members, func(i, j int) bool { return members[i].Manager < members[j].Manager })
		first := members[0]
		category := maintenanceCategory(first)
		apply := first.ApplySupported && !first.Stale && !hostedComponent(first) && (category == "update" || category == "refresh")
		ids := make([]string, 0, len(members))
		reason := first.Recommendation
		for _, h := range members {
			ids = append(ids, h.Manager)
			if h.Strategy != first.Strategy || h.Version != first.Version || h.CandidateVersion != first.CandidateVersion || h.ApplySupported != first.ApplySupported || h.UpdateStatus != first.UpdateStatus || h.Stale {
				apply = false
				if len(members) > 1 {
					category = "guidance"
					reason = "Shared target observations disagree or are stale; refresh checks before planning this target."
				}
			}
		}
		target := first.Path
		if first.OwnerPath != "" {
			target = first.OwnerPath
		}
		if first.OwnerPackage != "" {
			target += " / " + first.OwnerPackage
		}
		q.Jobs = append(q.Jobs, domain.MaintenanceJob{ID: hash(key)[:24], Category: category, Title: strings.Join(ids, ", "), Representative: first.Manager, ManagerIDs: ids, Target: target, Health: members, ApplySupported: apply, Reason: reason})
	}
	rank := map[string]int{"update": 0, "refresh": 1, "repair": 2, "guidance": 3, "missing": 4, "current": 5}
	sort.Slice(q.Jobs, func(i, j int) bool {
		a, b := q.Jobs[i], q.Jobs[j]
		if rank[a.Category] != rank[b.Category] {
			return rank[a.Category] < rank[b.Category]
		}
		if a.Title != b.Title {
			return a.Title < b.Title
		}
		return a.ID < b.ID
	})
	return q
}
func maintenanceCategory(h domain.ManagerHealth) string {
	if h.Path == "" || h.UpdateStatus == "missing" {
		return "missing"
	}
	if h.ApplySupported && !h.Stale && !hostedComponent(h) {
		if h.UpdateStatus == "refresh-available" {
			return "refresh"
		}
		if h.UpdateStatus == "available" {
			return "update"
		}
	}
	if !h.Compatible || strings.HasPrefix(h.ReasonCode, "component_") || h.ReasonCode == "parser_mismatch" || h.ReasonCode == "probe_failed" {
		return "repair"
	}
	if h.UpdateStatus == "current" {
		return "current"
	}
	return "guidance"
}
func provenTarget(h domain.ManagerHealth) string {
	if h.ComponentKind != "" && h.ComponentKind != "executable" || h.VersionSubject == "launcher" || h.OwnerPath == "" {
		return ""
	}
	if (h.Manager == "brew" || h.Manager == "cask") && h.Strategy == "brew-update" && h.Owner == "self" && filepath.Clean(h.Path) == filepath.Clean(h.OwnerPath) {
		return "brew\x00" + filepath.Clean(h.OwnerPath)
	}
	if h.Manager == "uvx" || h.Manager == "uv-pip" {
		if h.Strategy == "uv-self" && h.Owner == "standalone" && filepath.Clean(h.Path) == filepath.Clean(h.OwnerPath) {
			return "uv-self\x00" + filepath.Clean(h.OwnerPath) + "\x00" + h.Prefix + "\x00" + h.Channel
		}
		if h.Strategy == "brew-package" && h.Owner == "brew" && h.OwnerPackage == "uv" {
			return "brew-uv\x00" + filepath.Clean(h.OwnerPath) + "\x00" + filepath.Clean(h.Path)
		}
	}
	return ""
}
