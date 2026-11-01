package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/deranjer/vigara/internal/restic"
)

func (h *Handler) verifiedRestic(target string) ([]byte, error) {
	if _, ok := supportedDownloads[target]; !ok || h.releaseDir == "" {
		return nil, errors.New("restic unavailable")
	}
	b, err := readBoundedFile(filepath.Join(h.releaseDir, "manifest.json"), 1<<20)
	if err != nil {
		return nil, err
	}
	var manifest releaseManifest
	if json.Unmarshal(b, &manifest) != nil || manifest.ResticVersion != restic.PinnedVersion {
		return nil, errors.New("unsupported bundled restic version")
	}
	entry, ok := manifest.ResticArtifacts[target]
	ext := ""
	if target == "windows-amd64" {
		ext = ".exe"
	}
	if !ok || entry.File != "restic-"+target+ext || len(entry.SHA256) != 64 {
		return nil, errors.New("restic unavailable")
	}
	data, err := readBoundedFile(filepath.Join(h.releaseDir, entry.File), 128<<20)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if len(data) == 0 || hex.EncodeToString(sum[:]) != entry.SHA256 {
		return nil, errors.New("restic digest mismatch")
	}
	return data, nil
}
