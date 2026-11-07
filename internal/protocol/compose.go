package protocol

const (
	MethodComposeImageTags     = "compose.images.tags"
	MethodComposeImageSources  = "compose.images.sources"
	MethodComposeImagePlan     = "compose.images.plan"
	MethodComposeValidate      = "compose.validate"
	MethodComposeDeploy        = "compose.deploy"
	MethodComposeRecreate      = "compose.recreate"
	MethodComposeStatus        = "compose.status"
	MethodComposeTargets       = "compose.targets"
	MethodComposeCreate        = "compose.create"
	MethodComposeRecipient     = "compose.recipient"
	MethodComposeDiscover      = "compose.discover"
	MethodComposeImportPreview = "compose.importPreview"
	MethodComposeImportPrepare = "compose.importPrepare"
	MethodComposeImportConfirm = "compose.importConfirm"
	MethodComposeRemove        = "compose.remove"
)

func init() {
	channelMethodTiers[MethodComposeRemove] = DockerModeDeploy
	channelMethodTiers[MethodComposeImageTags] = DockerModeDeploy
	channelMethodTiers[MethodComposeImageSources] = DockerModeDeploy
	channelMethodTiers[MethodComposeRecreate] = DockerModeDeploy
	for _, method := range []string{MethodComposeImagePlan, MethodComposeValidate, MethodComposeDeploy, MethodComposeStatus, MethodComposeTargets, MethodComposeCreate, MethodComposeRecipient, MethodComposeDiscover, MethodComposeImportPreview, MethodComposeImportPrepare, MethodComposeImportConfirm} {
		channelMethodTiers[method] = DockerModeDeploy
	}
}

type ComposeImageTagsRequest struct {
	Image string `json:"image"`
	Last  string `json:"last,omitempty"`
}

type ComposeImageTags struct {
	Repository string   `json:"repository"`
	Tags       []string `json:"tags"`
	Next       string   `json:"next,omitempty"`
}

type ComposeImportCandidate struct {
	Directory        string   `json:"directory"`
	ProjectName      string   `json:"projectName"`
	Token            string   `json:"token"`
	State            string   `json:"state"`
	Containers       int      `json:"containers"`
	ComposeFiles     []string `json:"composeFiles"`
	EnvironmentFiles []string `json:"environmentFiles"`
	Issues           []string `json:"issues"`
	Imported         bool     `json:"imported"`
	Storage          []string `json:"storage"`
}
type ComposeDiscovery struct {
	Root       string                   `json:"root"`
	Candidates []ComposeImportCandidate `json:"candidates"`
}
type ComposeImportRequest struct {
	Directory         string `json:"directory"`
	Token             string `json:"token"`
	ID                string `json:"id"`
	Name              string `json:"name"`
	EditorRecipient   string `json:"editorRecipient"`
	RecoveryRecipient string `json:"recoveryRecipient"`
}
type ComposeImportPreview struct {
	Candidate ComposeImportCandidate `json:"candidate"`
	Files     map[string]string      `json:"files"`
}
type ComposeImportPrepared struct {
	Target         ComposeTargetInfo `json:"target"`
	Files          map[string]string `json:"files"`
	AgentRecipient string            `json:"agentRecipient"`
}

// ComposeRemoveRequest asks the agent to stop and remove a project's containers
// and networks. Volumes and host files are never deleted.
type ComposeRemoveRequest struct {
	StackID     string `json:"stackId"`
	ProjectName string `json:"projectName"`
}

type ComposeCreateTarget struct {
	ID          string `json:"id"`
	ProjectName string `json:"projectName"`
}
type ComposeRecipient struct {
	Recipient string `json:"recipient"`
}
type ComposeTargetInfo struct {
	ID          string `json:"id"`
	ProjectName string `json:"projectName"`
	Directory   string `json:"directory"`
}
type ComposeTargets struct {
	ManagedRoot string              `json:"managedRoot"`
	Targets     []ComposeTargetInfo `json:"targets"`
}
