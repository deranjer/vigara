package stack

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"
)

// ConfigurationRevision implements v1 for the supported regular 100644 files.
// The Git reader rejects other modes before creating these snapshots.
func ConfigurationRevision(s Snapshot) string {
	files := map[string]string{}
	for name, content := range s.Files {
		files["stacks/"+s.ID+"/"+name] = content
	}
	if p := s.Placement; p != nil {
		files["profiles/"+p.ProfileID+"/vigara-profile.yaml"] = p.ProfileManifest
		files["assignments/"+p.ProfileID+"/"+s.ID+".yaml"] = p.AssignmentManifest
	}
	keys := make([]string, 0, len(files))
	for name := range files {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	h := sha256.New()
	h.Write([]byte("VIGIRA-CONFIG-REVISION-V1\x00"))
	for _, name := range keys {
		h.Write([]byte("100644"))
		_ = binary.Write(h, binary.BigEndian, uint64(len(name)))
		h.Write([]byte(name))
		_ = binary.Write(h, binary.BigEndian, uint64(len(files[name])))
		h.Write([]byte(files[name]))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}
