package protocol

const (
	MethodDockerFilesList  = "docker.files.list"
	MethodDockerFilesRead  = "docker.files.read"
	MethodDockerFilesWrite = "docker.files.write"
	MaxDockerFileBytes     = 64 << 10
	MaxDockerFileEntries   = 500
	ErrCodeFileAccess      = "file_access"
)

func init() {
	// Files may contain credentials or control a host service. Require the
	// enrollment's exec grant even for reads, in addition to local opt-in.
	for _, method := range []string{MethodDockerFilesList, MethodDockerFilesRead, MethodDockerFilesWrite} {
		channelMethodTiers[method] = DockerModeExec
	}
}

// DockerFileParams identifies Docker storage, never an arbitrary host path.
// Kind is mount (ID = container, Mount = exact destination) or volume (ID = name).
type DockerFileParams struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Mount   string `json:"mount,omitempty"`
	Path    string `json:"path,omitempty"`
	Content string `json:"content,omitempty"`
	Version string `json:"version,omitempty"`
}

type DockerFileEntry struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Size int64  `json:"size"`
}

type DockerFilesResult struct {
	Entries    []DockerFileEntry `json:"entries"`
	SingleFile bool              `json:"singleFile"`
	Writable   bool              `json:"writable"`
	Truncated  bool              `json:"truncated,omitempty"`
}

type DockerFileResult struct {
	Content  string `json:"content"`
	Version  string `json:"version"`
	Writable bool   `json:"writable"`
	BackedUp bool   `json:"backedUp,omitempty"`
}
