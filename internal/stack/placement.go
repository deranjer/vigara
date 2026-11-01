package stack

// Placement carries the exact saved manifests reviewed for an assignment.
// Enrollment binding remains server-owned and cannot be granted by Git.
type Placement struct {
	ProfileID          string `json:"profileId"`
	AssignmentID       string `json:"assignmentId"`
	Epoch              uint64 `json:"epoch"`
	ProfileManifest    string `json:"profileManifest"`
	AssignmentManifest string `json:"assignmentManifest"`
}
