package dockerapi

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/deranjer/vigara/internal/protocol"
)

func TestStorageSourceUsesInspectedMountsAndRejectsUnsupportedVolumes(t *testing.T) {
	dir := t.TempDir()
	client, _ := newEngine(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected mutation: %s", r.Method)
		}
		switch r.URL.Path {
		case "/v1.44/containers/caddy/json":
			_ = json.NewEncoder(w).Encode(map[string]any{"Mounts": []map[string]string{
				{"Type": "bind", "Source": dir, "Destination": "/etc/caddy"},
				{"Type": "volume", "Name": "data", "Source": dir, "Destination": "/data"},
				{"Type": "tmpfs", "Destination": "/tmp"},
				{"Type": "volume", "Name": "data", "Source": filepath.Join(dir, "sub"), "Destination": "/sub"},
			}})
		case "/v1.44/volumes/data":
			_ = json.NewEncoder(w).Encode(volumeJSON{Driver: "local", Mountpoint: dir})
		case "/v1.44/volumes/nfs":
			_ = json.NewEncoder(w).Encode(volumeJSON{Driver: "local", Mountpoint: dir, Options: map[string]string{"type": "nfs"}})
		case "/v1.44/volumes/plugin":
			_ = json.NewEncoder(w).Encode(volumeJSON{Driver: "plugin", Mountpoint: dir})
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	for _, p := range []protocol.DockerFileParams{{Kind: "mount", ID: "caddy", Mount: "/etc/caddy"}, {Kind: "mount", ID: "caddy", Mount: "/data"}, {Kind: "volume", ID: "data"}} {
		if source, err := client.StorageSource(context.Background(), p); err != nil || source != dir {
			t.Fatalf("source = %q, %v", source, err)
		}
	}
	for _, p := range []protocol.DockerFileParams{{Kind: "mount", ID: "caddy", Mount: dir}, {Kind: "mount", ID: "caddy", Mount: "/tmp"}, {Kind: "mount", ID: "caddy", Mount: "/sub"}, {Kind: "volume", ID: "nfs"}, {Kind: "volume", ID: "plugin"}, {Kind: "host", ID: dir}, {Kind: "volume", ID: "../../etc"}} {
		if source, err := client.StorageSource(context.Background(), p); err == nil {
			t.Fatalf("unexpectedly resolved %+v to %s", p, source)
		}
	}
}
