package api

import (
	"testing"

	"github.com/dennisme/grex/internal/fleet"
)

func TestPartitionJobTargetsAcceptsSupervisorManaged(t *testing.T) {
	agents := []fleet.Agent{
		{InstanceUID: "agent-1", NonIdentifying: map[string]string{"opamp.managed_by": "opentelemetry-opampsupervisor"}},
	}
	accepted, rejected := PartitionJobTargets(agents)
	if len(rejected) != 0 {
		t.Fatalf("rejected = %v, want none", rejected)
	}
	if len(accepted) != 1 || accepted[0] != "agent-1" {
		t.Errorf("accepted = %v, want [agent-1]", accepted)
	}
}

func TestPartitionJobTargetsRejectsNotSupervisorManaged(t *testing.T) {
	agents := []fleet.Agent{
		{InstanceUID: "agent-1"}, // bare opamp extension, no opamp.managed_by
	}
	accepted, rejected := PartitionJobTargets(agents)
	if len(accepted) != 0 {
		t.Fatalf("accepted = %v, want none", accepted)
	}
	if len(rejected) != 1 {
		t.Fatalf("rejected = %v, want 1 entry", rejected)
	}
	if rejected[0].InstanceUID != "agent-1" {
		t.Errorf("rejected[0].InstanceUID = %q, want agent-1", rejected[0].InstanceUID)
	}
	if rejected[0].Reason != RejectionReasonNotSupervisorManaged {
		t.Errorf("rejected[0].Reason = %q, want %q", rejected[0].Reason, RejectionReasonNotSupervisorManaged)
	}
}

func TestPartitionJobTargetsMixed(t *testing.T) {
	agents := []fleet.Agent{
		{InstanceUID: "agent-1", NonIdentifying: map[string]string{"opamp.managed_by": "opentelemetry-opampsupervisor"}},
		{InstanceUID: "agent-2"},
		{InstanceUID: "agent-3", NonIdentifying: map[string]string{"opamp.managed_by": "opentelemetry-opampsupervisor"}},
	}
	accepted, rejected := PartitionJobTargets(agents)
	if len(accepted) != 2 || accepted[0] != "agent-1" || accepted[1] != "agent-3" {
		t.Errorf("accepted = %v, want [agent-1 agent-3]", accepted)
	}
	if len(rejected) != 1 || rejected[0].InstanceUID != "agent-2" {
		t.Errorf("rejected = %v, want [{agent-2 ...}]", rejected)
	}
}

func TestPartitionJobTargetsEmpty(t *testing.T) {
	accepted, rejected := PartitionJobTargets(nil)
	if len(accepted) != 0 || len(rejected) != 0 {
		t.Errorf("accepted=%v rejected=%v, want both empty", accepted, rejected)
	}
}
