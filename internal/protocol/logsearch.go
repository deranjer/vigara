package protocol

const MethodDockerLogSearch = "docker.logs.search"

func init() { channelMethodTiers[MethodDockerLogSearch] = "" }

type DockerLogSearchParams struct {
	ID    string `json:"id"`
	Match string `json:"match"`
	Since string `json:"since,omitempty"`
	Until string `json:"until,omitempty"`
}
type DockerLogSearchResult struct {
	Lines        []DockerLogLine `json:"lines"`
	Truncated    bool            `json:"truncated"`
	MoreMatches  bool            `json:"moreMatches"`
	NextSince    string          `json:"nextSince,omitempty"`
	ScannedBytes int64           `json:"scannedBytes"`
}
