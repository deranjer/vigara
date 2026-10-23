package protocol

const (
	MethodComposeValidate = "compose.validate"
	MethodComposeDeploy   = "compose.deploy"
	MethodComposeStatus   = "compose.status"
	MethodComposeTargets  = "compose.targets"
	MethodComposeCreate   = "compose.create"
)

func init() {
	for _, method := range []string{MethodComposeValidate, MethodComposeDeploy, MethodComposeStatus, MethodComposeTargets, MethodComposeCreate} {
		channelMethodTiers[method] = DockerModeDeploy
	}
}

type ComposeCreateTarget struct {
	ID          string `json:"id"`
	ProjectName string `json:"projectName"`
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
