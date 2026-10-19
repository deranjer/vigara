package protocol

// Docker management methods carried over the live channel. They change the host,
// so both the server and the agent require the manage tier before they run.
const (
	MethodDockerAction   = "docker.action"   // call: one lifecycle action on one container
	MethodDockerRecreate = "docker.recreate" // stream: replace a container with a fresh copy, optionally on a newer image
)

func init() {
	channelMethodTiers[MethodDockerAction] = DockerModeManage
	channelMethodTiers[MethodDockerRecreate] = DockerModeManage
}

// ErrCodeConflict reports that Docker refused an action in the container's
// current state, for example removing a running container without force.
const ErrCodeConflict = "conflict"

// ErrCodeRecreateFailed reports a recreate that did not complete; its message says
// which step failed and whether the original container was restored.
const ErrCodeRecreateFailed = "recreate_failed"

// Lifecycle actions.
const (
	ActionStart    = "start"
	ActionStop     = "stop"
	ActionRestart  = "restart"
	ActionPause    = "pause"
	ActionUnpause  = "unpause"
	ActionKill     = "kill"
	ActionRemove   = "remove"
	ActionRecreate = "recreate" // used in audit records for MethodDockerRecreate
)

// DockerActions lists the lifecycle actions MethodDockerAction accepts.
var DockerActions = []string{ActionStart, ActionStop, ActionRestart, ActionPause, ActionUnpause, ActionKill, ActionRemove}

// DockerSignals lists the signals kill may send.
var DockerSignals = []string{"KILL", "TERM", "HUP", "INT", "QUIT", "USR1", "USR2", "STOP", "CONT"}

// Bounds on how long Docker is asked to wait for a container to stop.
const (
	DefaultStopSeconds = 10
	MaxStopSeconds     = 300
)

// DockerActionParams selects one lifecycle action. Force and RemoveVolumes apply
// only to remove (Force also lets it delete a running container), TimeoutSeconds to
// stop and restart, and Signal to kill.
type DockerActionParams struct {
	ID             string `json:"id"`
	Action         string `json:"action"`
	Force          bool   `json:"force,omitempty"`
	RemoveVolumes  bool   `json:"removeVolumes,omitempty"`
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty"`
	Signal         string `json:"signal,omitempty"`
}

// DockerActionResult describes what happened. Changed is false when the
// container was already in the requested state.
type DockerActionResult struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Action  string `json:"action"`
	Changed bool   `json:"changed"`
	Message string `json:"message,omitempty"`
}

// DockerRecreateParams starts a recreate. Pull fetches the container's image
// reference again first, so a moving tag such as "latest" can pick up a new image.
type DockerRecreateParams struct {
	ID   string `json:"id"`
	Pull bool   `json:"pull,omitempty"`
}

// Steps reported while recreating a container.
const (
	StepPull      = "pull"
	StepCreate    = "create"
	StepStopOld   = "stop_old"
	StepSwap      = "swap"
	StepStart     = "start"
	StepVerify    = "verify"
	StepCleanup   = "cleanup"
	StepRollback  = "rollback"
	StepDone      = "done"
	StepSkipped   = "skipped" // informational: a step did not apply
	StepPullLayer = "pull_layer"
)

// DockerRecreateEvent is one record of the recreate stream (newline-delimited
// JSON). The stream ends with a StepDone record carrying Result on success; a
// failure ends the stream with an error instead.
type DockerRecreateEvent struct {
	Step    string                `json:"step"`
	Message string                `json:"message,omitempty"`
	Layer   string                `json:"layer,omitempty"`
	Current int64                 `json:"current,omitempty"`
	Total   int64                 `json:"total,omitempty"`
	Result  *DockerRecreateResult `json:"result,omitempty"`
}

// DockerRecreateResult summarises a finished recreate.
type DockerRecreateResult struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	PreviousImageID string `json:"previousImageId,omitempty"`
	ImageID         string `json:"imageId,omitempty"`
	ImageChanged    bool   `json:"imageChanged"`
	Started         bool   `json:"started"`
	RolledBack      bool   `json:"rolledBack,omitempty"`
}
