package fleet

import "encoding/json"

// MarshalJSON adds the decoded capability_flags alongside the raw
// capabilities bitmask, so API consumers get named booleans without
// bitmask math.
func (a Agent) MarshalJSON() ([]byte, error) {
	type alias Agent
	return json.Marshal(struct {
		alias
		CapabilityFlags Capabilities `json:"capability_flags"`
	}{
		alias:           alias(a),
		CapabilityFlags: a.DecodedCapabilities(),
	})
}
