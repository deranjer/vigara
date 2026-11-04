package dockerapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/deranjer/vigara/internal/protocol"
)

type updateTransport func(*http.Request) (*http.Response, error)

func (f updateTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestImageCheckComparesRunningConfigAndSelectsPlatform(t *testing.T) {
	old := "sha256:" + strings.Repeat("a", 64)
	next := "sha256:" + strings.Repeat("b", 64)
	manifest := "sha256:" + strings.Repeat("c", 64)
	for _, tc := range []struct{ name, ref, remote, want string }{
		{"tag-retained-in-pin", "app:stable@" + manifest, next, "available"},
		{"unchanged", "app:stable", old, "current"}, {"changed", "app:1.2.3", next, "available"}, {"pinned", "app@" + manifest, next, "pinned"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := New("http://127.0.0.1", &http.Client{Transport: updateTransport(func(req *http.Request) (*http.Response, error) {
				var data []byte
				switch req.URL.Path {
				case "/v1.44/containers/web/json":
					data, _ = json.Marshal(map[string]any{"Id": "web", "Image": old, "Name": "/web", "Config": map[string]any{"Image": tc.ref}})
				case "/v1.44/images/" + old + "/json":
					data = []byte(`{"Os":"linux","Architecture":"amd64"}`)
				default:
					t.Fatal("unexpected request", req.URL)
					return nil, fmt.Errorf("unexpected request")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(data)))}, nil
			})})
			calls := 0
			result, err := client.imageCheck(context.Background(), protocol.DockerImageCheckParams{ID: "web"}, func(_ context.Context, ref string) ([]byte, error) {
				calls++
				if calls == 1 {
					return []byte(`{"manifests":[{"digest":"sha256:` + strings.Repeat("d", 64) + `","platform":{"os":"linux","architecture":"arm64"}},{"digest":"` + manifest + `","platform":{"os":"linux","architecture":"amd64"}}]}`), nil
				}
				if ref != "app@"+manifest {
					t.Fatal("selected wrong platform", ref)
				}
				return []byte(`{"config":{"digest":"` + tc.remote + `"}}`), nil
			})
			if err != nil || result.Status != tc.want {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if tc.want == "pinned" && calls != 0 {
				t.Fatal("pinned image queried registry")
			}
		})
	}
}
func TestManagedTrackingTagAndRegistryFailures(t *testing.T) {
	old := "sha256:" + strings.Repeat("a", 64)
	client, _ := New("http://127.0.0.1", &http.Client{Transport: updateTransport(func(req *http.Request) (*http.Response, error) {
		data := `{"Os":"linux","Architecture":"amd64"}`
		if strings.Contains(req.URL.Path, "containers") {
			data = `{"Id":"web","Image":"` + old + `","Config":{"Image":"app@` + old + `"}}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(data))}, nil
	})})
	result, err := client.imageCheck(context.Background(), protocol.DockerImageCheckParams{ID: "web", Ref: "app:17"}, func(_ context.Context, ref string) ([]byte, error) {
		if ref != "app:17" {
			t.Fatal(ref)
		}
		return nil, fmt.Errorf("registry unavailable")
	})
	if err != nil || result.Image != "app:17" || result.Status != "unknown" {
		t.Fatalf("%+v %v", result, err)
	}
	if _, err = client.imageCheck(context.Background(), protocol.DockerImageCheckParams{ID: "../bad"}, nil); err != ErrBadRequest {
		t.Fatal("invalid container reference accepted")
	}
}

func TestRegistryDescriptorDigestIsPreservedForSingleAndOCIImages(t *testing.T) {
	digest := "sha256:" + strings.Repeat("e", 64)
	config := "sha256:" + strings.Repeat("a", 64)
	for _, kind := range []string{"SchemaV2Manifest", "OCIManifest"} {
		data := []byte(`{"Descriptor":{"digest":"` + digest + `","platform":{"os":"linux","architecture":"amd64"}},"` + kind + `":{"config":{"digest":"` + config + `"}}}`)
		for _, raw := range [][]byte{data, append(append([]byte("["), data...), ']')} {
			out, err := normalizeRegistryManifest(raw)
			if err != nil {
				t.Fatal(err)
			}
			var m imageManifest
			if json.Unmarshal(out, &m) != nil {
				t.Fatal(string(out))
			}
			if len(m.Manifests) > 0 {
				if m.Manifests[0].Digest != digest {
					t.Fatal(string(out))
				}
			} else if m.Digest != digest || m.Config.Digest != config {
				t.Fatal("CLI formatting changed the digest", string(out))
			}
		}
	}
}
