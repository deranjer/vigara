package protocol

// DeviceIdentifier is a stable, platform supplied identifier for a physical
// or virtual device. Values are treated as untrusted input and normalized by
// the server before reconciliation.
type DeviceIdentifier struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}
