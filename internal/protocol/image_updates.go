package protocol

const MethodDockerImageCheck = "docker.image.check"

func init() { channelMethodTiers[MethodDockerImageCheck] = "" }

// Ref is optional and is used only to recover a managed stack's original tag
// after deployment has replaced it with an immutable digest.
type DockerImageCheckParams struct {
	ID  string `json:"id"`
	Ref string `json:"ref,omitempty"`
}

type DockerImageCheck struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Image     string `json:"image"`
	Project   string `json:"project,omitempty"`
	Service   string `json:"service,omitempty"`
	Platform  string `json:"platform,omitempty"`
	Current   string `json:"current,omitempty"`
	Available string `json:"available,omitempty"`
	Manifest  string `json:"manifest,omitempty"`
	Status    string `json:"status"` // current, available, pinned, unknown
	Message   string `json:"message,omitempty"`
}
