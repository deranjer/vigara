package protocol

// ManagedBackup is delivered only to an agent that opted into server management.
// Passwords must never be logged or included in the ordinary configuration cache.
type ManagedBackup struct {
	Revision    int64    `json:"revision"`
	Repository  string   `json:"repository"`
	SourcePaths []string `json:"sourcePaths"`
	Password    string   `json:"password"`
	Initialize  bool     `json:"initialize"`
}

type ManagedBackupStatus struct {
	Revision  int64  `json:"revision"`
	ErrorCode string `json:"errorCode,omitempty"`
}
