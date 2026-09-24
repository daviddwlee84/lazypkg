package maintenance

import (
	"reflect"
	"testing"
	"time"

	"github.com/daviddwlee84/lazypkg/internal/domain"
)

func TestQueueGroupsOnlyProvenSharedTargets(t *testing.T) {
	brew := domain.ManagerHealth{Manager: "brew", Path: "/opt/bin/brew", OwnerPath: "/opt/bin/brew", Owner: "self", Version: "4.0.0", CandidateVersion: "4.1.0", Strategy: "brew-update", UpdateStatus: "available", Compatible: true, ApplySupported: true}
	cask := brew
	cask.Manager = "cask"
	uv := domain.ManagerHealth{Manager: "uvx", Path: "/home/bin/uv", OwnerPath: "/home/bin/uv", Owner: "standalone", Version: "0.9.0", CandidateVersion: "0.10.0", Prefix: "/home/bin", Channel: "stable", Strategy: "uv-self", UpdateStatus: "available", Compatible: true, ApplySupported: true}
	pip := uv
	pip.Manager = "uv-pip"
	q := BuildQueue([]domain.ManagerHealth{pip, cask, uv, brew}, time.Time{})
	if len(q.Jobs) != 2 || !reflect.DeepEqual(q.Jobs[0].ManagerIDs, []string{"brew", "cask"}) || !reflect.DeepEqual(q.Jobs[1].ManagerIDs, []string{"uv-pip", "uvx"}) || !q.Jobs[0].ApplySupported || !q.Jobs[1].ApplySupported {
		t.Fatal(q)
	}
	ids := []string{q.Jobs[0].ID, q.Jobs[1].ID}
	brew.CandidateVersion, cask.CandidateVersion = "4.2.0", "4.2.0"
	uv.CandidateVersion, pip.CandidateVersion = "0.11.0", "0.11.0"
	fresh := BuildQueue([]domain.ManagerHealth{brew, cask, uv, pip}, time.Now())
	if ids[0] != fresh.Jobs[0].ID || ids[1] != fresh.Jobs[1].ID {
		t.Fatal("job IDs changed after candidate refresh")
	}
	for _, change := range []func(*domain.ManagerHealth){
		func(h *domain.ManagerHealth) { h.OwnerPath = "/other/uv" },
		func(h *domain.ManagerHealth) { h.Prefix = "/other/prefix" },
		func(h *domain.ManagerHealth) { h.Owner = "unknown" },
		func(h *domain.ManagerHealth) { h.Strategy = "" },
	} {
		other := pip
		change(&other)
		if got := BuildQueue([]domain.ManagerHealth{uv, other}, time.Time{}); len(got.Jobs) != 2 {
			t.Fatal("unproven targets merged", got)
		}
	}
}

func TestQueueBlocksDisagreementAndHostedTargets(t *testing.T) {
	base := domain.ManagerHealth{Manager: "uvx", Path: "/uv", OwnerPath: "/brew", Owner: "brew", OwnerPackage: "uv", Version: "1.0.0", CandidateVersion: "2.0.0", Strategy: "brew-package", UpdateStatus: "available", Compatible: true, ApplySupported: true}
	other := base
	other.Manager, other.CandidateVersion = "uv-pip", "3.0.0"
	q := BuildQueue([]domain.ManagerHealth{base, other}, time.Time{})
	if len(q.Jobs) != 1 || q.Jobs[0].ApplySupported || q.Jobs[0].Category != "guidance" {
		t.Fatal(q)
	}
	plugin := base
	plugin.Manager = "fisher"
	q = BuildQueue([]domain.ManagerHealth{plugin}, time.Time{})
	if q.Jobs[0].ApplySupported || q.Jobs[0].Category == "update" {
		t.Fatal("hosted manager actionable", q)
	}
	base.Alternatives = []string{"/other/uv"}
	q = BuildQueue([]domain.ManagerHealth{base}, time.Time{})
	q.Jobs[0].Health[0].Alternatives[0] = "changed"
	if base.Alternatives[0] != "/other/uv" {
		t.Fatal("queue exposed input backing array")
	}
}
