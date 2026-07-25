package fleet

import (
	"strings"
	"time"
)

// AgentView is an agent snapshot with presentation helpers for the API and UI.
// List responses omit bulky fields (effective config, packages); detail
// responses include them.
type AgentView struct {
	InstanceUID         string             `json:"instance_uid"`
	SequenceNum         uint64             `json:"sequence_num"`
	Identifying         map[string]string  `json:"identifying_attributes,omitempty"`
	NonIdentifying      map[string]string  `json:"non_identifying_attributes,omitempty"`
	Capabilities        uint64             `json:"capabilities"`
	CapabilityFlags     Capabilities       `json:"capability_flags"`
	Healthy             bool               `json:"healthy"`
	HealthError         string             `json:"health_error,omitempty"`
	HealthStatus        string             `json:"health_status,omitempty"`
	HealthStartTime     time.Time          `json:"health_start_time,omitempty"`
	HealthStatusTime    time.Time          `json:"health_status_time,omitempty"`
	HealthReported      bool               `json:"health_reported"`
	DescriptionReported bool               `json:"description_reported"`
	EffectiveConfig     map[string]string  `json:"effective_config,omitempty"`
	Packages            map[string]Package `json:"packages,omitempty"`
	Conn                ConnMeta           `json:"connection"`
	Connected           bool               `json:"connected"`
	FirstSeen           time.Time          `json:"first_seen"`
	LastSeen            time.Time          `json:"last_seen"`
	MissingAttributes   []string           `json:"missing_attributes,omitempty"`
	// Role is a best-effort collector role (see RoleOf).
	Role string `json:"role"`
	// DisplayName is the primary human label for tables.
	DisplayName string `json:"display_name"`
	// HostName is host.name when reported.
	HostName string `json:"host_name,omitempty"`
	// Version is service.version when reported.
	Version string `json:"version,omitempty"`
}

// SummaryView returns a compact view for list endpoints (no config/packages).
func SummaryView(a Agent) AgentView {
	v := fullView(a)
	v.EffectiveConfig = nil
	v.Packages = nil
	return v
}

// DetailView returns the full agent view including config and packages.
func DetailView(a Agent) AgentView {
	return fullView(a)
}

func fullView(a Agent) AgentView {
	return AgentView{
		InstanceUID:         a.InstanceUID,
		SequenceNum:         a.SequenceNum,
		Identifying:         a.Identifying,
		NonIdentifying:      a.NonIdentifying,
		Capabilities:        a.Capabilities,
		CapabilityFlags:     a.DecodedCapabilities(),
		Healthy:             a.Healthy,
		HealthError:         a.HealthError,
		HealthStatus:        a.HealthStatus,
		HealthStartTime:     a.HealthStartTime,
		HealthStatusTime:    a.HealthStatusTime,
		HealthReported:      a.HealthReported,
		DescriptionReported: a.DescriptionReported,
		EffectiveConfig:     a.EffectiveConfig,
		Packages:            a.Packages,
		Conn:                a.Conn,
		Connected:           a.Connected,
		FirstSeen:           a.FirstSeen,
		LastSeen:            a.LastSeen,
		MissingAttributes:   a.MissingAttributes,
		Role:                RoleOf(a),
		DisplayName:         DisplayNameOf(a),
		HostName:            Attr(a, "host.name"),
		Version:             Attr(a, "service.version"),
	}
}

// Attr returns an identifying or non-identifying attribute value.
func Attr(a Agent, key string) string {
	if v, ok := a.Identifying[key]; ok {
		return v
	}
	return a.NonIdentifying[key]
}

// RoleOf computes the best-effort role string for an agent.
// 1. service.component when set
// 2. "Gateway" if service.name contains "gateway" (case-insensitive)
// 3. "Collector" otherwise
func RoleOf(a Agent) string {
	if c := Attr(a, "service.component"); c != "" {
		return c
	}
	name := strings.ToLower(Attr(a, "service.name"))
	if strings.Contains(name, "gateway") {
		return "Gateway"
	}
	return "Collector"
}

// DisplayNameOf prefers service.name, then host.name, then instance_uid.
func DisplayNameOf(a Agent) string {
	if n := Attr(a, "service.name"); n != "" {
		return n
	}
	if n := Attr(a, "host.name"); n != "" {
		return n
	}
	return a.InstanceUID
}
