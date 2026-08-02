package api

import "github.com/dennisme/grex/internal/fleet"

// RejectionReasonNotSupervisorManaged is why PartitionJobTargets rejects a
// matched agent that hasn't declared fleet.SupervisorManaged.
const RejectionReasonNotSupervisorManaged = "not supervisor_managed"

// RejectedTarget is one matched agent a job will not actually target, and
// why.
type RejectedTarget struct {
	InstanceUID string
	Reason      string
}

// PartitionJobTargets splits a job's matched agents into instance_uids it
// may actually target and ones rejected with a reason, per
// docs/spec/design.md's "Decided: per-target rejection with a reason": one
// non-compliant agent in a filter is excluded, not silently dropped and not
// a reason to fail the whole job. Only rule today: reject an agent that
// isn't fleet.SupervisorManaged. A future rule (e.g. a blast-radius cap)
// adds another branch here, not a different signature.
func PartitionJobTargets(agents []fleet.Agent) (accepted []string, rejected []RejectedTarget) {
	for _, a := range agents {
		if !fleet.SupervisorManaged(a) {
			rejected = append(rejected, RejectedTarget{InstanceUID: a.InstanceUID, Reason: RejectionReasonNotSupervisorManaged})
			continue
		}
		accepted = append(accepted, a.InstanceUID)
	}
	return accepted, rejected
}
