package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/deranjer/vigara/internal/protocol"
	"github.com/deranjer/vigara/internal/serverurl"
)

// HostAgentOptions are supplied by an operator through the local server CLI.
// This operation is deliberately unavailable through the unauthenticated API.
type HostAgentOptions struct {
	Name, Target, ServerURL, DockerHost, ManagedRoot string
}

// WriteHostAgentBundle reuses the normal single-use, expiring enrollment flow.
// Access to the server container and its database credentials is required.
func (h *Handler) WriteHostAgentBundle(ctx context.Context, output io.Writer, options HostAgentOptions) error {
	if options.Target != "linux-amd64" && options.Target != "linux-arm64" {
		return errors.New("host enrollment requires Linux amd64 or arm64")
	}
	if !strings.HasPrefix(options.DockerHost, "unix://") {
		return errors.New("host enrollment requires a local Docker Unix socket")
	}
	if err := serverurl.Validate(options.ServerURL, true); err != nil {
		return err
	}
	settings := &dockerSetup{Mode: protocol.DockerModeDeploy, Host: options.DockerHost, ManagedRoot: options.ManagedRoot}
	if err := settings.validate(options.Target); err != nil {
		return err
	}
	if _, _, err := h.verifiedArtifact(options.Target); err != nil && h.releaseStore != nil {
		if err := h.releaseStore.sync(ctx); err != nil {
			return fmt.Errorf("prepare host agent release: %w", err)
		}
	}
	// Use a private handler so the local origin never changes the URLs approved
	// for downloads through the web API.
	local := &Handler{pool: h.pool, releaseDir: h.releaseDir, releaseStore: h.releaseStore, serverURLs: []string{options.ServerURL}}
	response := &hostBundleResponse{header: make(http.Header)}
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/", nil)
	local.writeAgentDownload(response, request, agentDownloadRequest{
		Name: options.Name, Target: options.Target, ServerURL: options.ServerURL, Docker: settings,
	})
	if response.status != http.StatusOK {
		return fmt.Errorf("host enrollment: %s", strings.TrimSpace(response.String()))
	}
	if _, err := zip.NewReader(bytes.NewReader(response.Bytes()), int64(response.Len())); err != nil {
		return errors.New("host enrollment bundle was incomplete; retry setup")
	}
	_, err := output.Write(response.Bytes())
	return err
}

type hostBundleResponse struct {
	bytes.Buffer
	header http.Header
	status int
}

func (w *hostBundleResponse) Header() http.Header    { return w.header }
func (w *hostBundleResponse) WriteHeader(status int) { w.status = status }
func (w *hostBundleResponse) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.Buffer.Write(data)
}
