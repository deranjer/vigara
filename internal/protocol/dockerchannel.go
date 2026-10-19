package protocol

// Docker read methods carried over the live channel. They need no management
// tier, but the agent offers them only when local Docker collection is enabled.
const (
	MethodDockerList    = "docker.list"    // call: every container on the host
	MethodDockerInspect = "docker.inspect" // call: one container's configuration and state
	MethodDockerLogs    = "docker.logs"    // stream: newline-delimited DockerLogLine JSON
	MethodDockerStats   = "docker.stats"   // stream: newline-delimited DockerStatsSample JSON
)

func init() {
	for _, method := range []string{MethodDockerList, MethodDockerInspect, MethodDockerLogs, MethodDockerStats} {
		channelMethodTiers[method] = ""
	}
}

// Limits that keep Docker responses bounded on both ends.
const (
	MaxDockerContainers = 2000
	MaxLogTail          = 5000
	DefaultLogTail      = 200
	MaxLogLineBytes     = 16 << 10
)

// DockerPort is one published or exposed container port.
type DockerPort struct {
	IP          string `json:"ip,omitempty"`
	PrivatePort uint16 `json:"privatePort"`
	PublicPort  uint16 `json:"publicPort,omitempty"`
	Type        string `json:"type"`
}

// DockerContainerInfo is one row of the container list.
type DockerContainerInfo struct {
	ID      string       `json:"id"`
	Name    string       `json:"name"`
	Image   string       `json:"image"`
	State   string       `json:"state"`
	Status  string       `json:"status"`
	Created int64        `json:"created"`
	Ports   []DockerPort `json:"ports,omitempty"`
	// Project and Service come from Docker Compose's labels and are empty for
	// containers that were not started by Compose.
	Project string `json:"project,omitempty"`
	Service string `json:"service,omitempty"`
}

// DockerListResult is the reply to MethodDockerList.
type DockerListResult struct {
	Containers []DockerContainerInfo `json:"containers"`
	Truncated  bool                  `json:"truncated,omitempty"`
}

// DockerInspectParams selects a container. Environment values are returned only
// when RevealEnv is set, which the server permits only for users who may reveal secrets.
type DockerInspectParams struct {
	ID        string `json:"id"`
	RevealEnv bool   `json:"revealEnv,omitempty"`
}

// DockerEnvVar is one environment variable; Value is empty unless revealed.
type DockerEnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
}

// DockerMount is one bind mount or volume.
type DockerMount struct {
	Type        string `json:"type"`
	Name        string `json:"name,omitempty"`
	Source      string `json:"source,omitempty"`
	Destination string `json:"destination"`
	ReadWrite   bool   `json:"readWrite"`
}

// DockerNetworkAttachment is a container's address on one network.
type DockerNetworkAttachment struct {
	Name       string   `json:"name"`
	IPAddress  string   `json:"ipAddress,omitempty"`
	Gateway    string   `json:"gateway,omitempty"`
	MacAddress string   `json:"macAddress,omitempty"`
	Aliases    []string `json:"aliases,omitempty"`
}

// DockerState is a container's runtime state.
type DockerState struct {
	Status     string `json:"status"`
	Running    bool   `json:"running"`
	Paused     bool   `json:"paused"`
	Restarting bool   `json:"restarting"`
	OOMKilled  bool   `json:"oomKilled"`
	ExitCode   int    `json:"exitCode"`
	Error      string `json:"error,omitempty"`
	StartedAt  string `json:"startedAt,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
	Health     string `json:"health,omitempty"`
	Pid        int    `json:"pid,omitempty"`
}

// DockerInspectResult is a curated, bounded view of a container. It deliberately
// omits fields that are large or rarely useful, and redacts environment values.
type DockerInspectResult struct {
	ID            string                    `json:"id"`
	Name          string                    `json:"name"`
	Image         string                    `json:"image"`
	ImageID       string                    `json:"imageId,omitempty"`
	Created       string                    `json:"created,omitempty"`
	State         DockerState               `json:"state"`
	Command       []string                  `json:"command,omitempty"`
	Entrypoint    []string                  `json:"entrypoint,omitempty"`
	WorkingDir    string                    `json:"workingDir,omitempty"`
	User          string                    `json:"user,omitempty"`
	Hostname      string                    `json:"hostname,omitempty"`
	TTY           bool                      `json:"tty"`
	Env           []DockerEnvVar            `json:"env"`
	EnvRevealed   bool                      `json:"envRevealed"`
	Labels        map[string]string         `json:"labels,omitempty"`
	Mounts        []DockerMount             `json:"mounts,omitempty"`
	Ports         []DockerPort              `json:"ports,omitempty"`
	Networks      []DockerNetworkAttachment `json:"networks,omitempty"`
	RestartPolicy string                    `json:"restartPolicy,omitempty"`
	NetworkMode   string                    `json:"networkMode,omitempty"`
	Privileged    bool                      `json:"privileged"`
	ReadonlyRoot  bool                      `json:"readonlyRoot"`
	MemoryLimit   int64                     `json:"memoryLimit,omitempty"`
	NanoCPUs      int64                     `json:"nanoCpus,omitempty"`
	CapAdd        []string                  `json:"capAdd,omitempty"`
	CapDrop       []string                  `json:"capDrop,omitempty"`
	LogDriver     string                    `json:"logDriver,omitempty"`
	RestartCount  int                       `json:"restartCount"`
}

// DockerLogsParams starts a log stream. Zero Tail uses DefaultLogTail.
type DockerLogsParams struct {
	ID         string `json:"id"`
	Tail       int    `json:"tail,omitempty"`
	Follow     bool   `json:"follow,omitempty"`
	Timestamps bool   `json:"timestamps,omitempty"`
	Since      string `json:"since,omitempty"`
}

// DockerLogLine is one log line. S is "o" for stdout or "e" for stderr; T is the
// Docker timestamp when requested; X marks a line cut at MaxLogLineBytes.
type DockerLogLine struct {
	S string `json:"s"`
	T string `json:"t,omitempty"`
	M string `json:"m"`
	X bool   `json:"x,omitempty"`
}

// DockerStatsParams starts a live statistics stream.
type DockerStatsParams struct {
	ID string `json:"id"`
}

// DockerStatsSample is one reading. Rates are omitted until a second sample exists.
type DockerStatsSample struct {
	T        string   `json:"t"`
	CPU      float64  `json:"cpu"`      // percent of one core, like `docker stats`
	CPUCores int      `json:"cpuCores"` // cores available to the host
	MemUsed  uint64   `json:"memUsed"`  // bytes, excluding reclaimable cache
	MemLimit uint64   `json:"memLimit"` // bytes
	MemPct   float64  `json:"memPct"`
	NetRx    uint64   `json:"netRx"` // cumulative bytes
	NetTx    uint64   `json:"netTx"`
	NetRxBps *float64 `json:"netRxBps,omitempty"`
	NetTxBps *float64 `json:"netTxBps,omitempty"`
	BlkRead  uint64   `json:"blkRead"` // cumulative bytes
	BlkWrite uint64   `json:"blkWrite"`
	Pids     uint64   `json:"pids"`
}
