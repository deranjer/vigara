package composedeploy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/deranjer/vigara/apps/agent/internal/channel"
	"github.com/deranjer/vigara/internal/protocol"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

var registryTag = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

func registerImageTags(calls map[string]channel.Handler) {
	calls[protocol.MethodComposeImageTags] = func(ctx context.Context, raw json.RawMessage) (any, error) {
		var q protocol.ComposeImageTagsRequest
		if json.Unmarshal(raw, &q) != nil || q.Image == "" || len(q.Image) > 512 || (q.Last != "" && !registryTag.MatchString(q.Last)) {
			return nil, &protocol.ChannelError{Code: protocol.ErrCodeBadRequest, Message: "Choose a valid image reference and tag."}
		}
		ref, err := name.ParseReference(q.Image)
		if err != nil {
			return nil, &protocol.ChannelError{Code: protocol.ErrCodeBadRequest, Message: "Choose a valid image reference."}
		}
		ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
		defer cancel()
		repo := ref.Context()
		auth, err := authn.Resolve(ctx, authn.DefaultKeychain, repo)
		var rt http.RoundTripper
		if err == nil {
			rt, err = transport.NewWithContext(ctx, repo.Registry, auth, http.DefaultTransport, []string{repo.Scope(transport.PullScope)})
		}
		var result protocol.ComposeImageTags
		if err == nil {
			result, err = listImageTags(ctx, repo, q.Last, &http.Client{Transport: rt})
		}
		if err != nil {
			// Registry errors can include credentials and URLs; return a fixed diagnostic.
			return nil, &protocol.ChannelError{Code: protocol.ErrCodeBadRequest, Message: "Could not list image tags. Check registry connectivity, rate limits and Docker login for the agent's account on this device. You can also enter a tag manually."}
		}
		return result, nil
	}
}

// Each request returns one registry page. Only a tag cursor crosses the channel,
// never a registry-provided URL, so pagination cannot redirect credentials.
func listImageTags(ctx context.Context, repo name.Repository, last string, client *http.Client) (protocol.ComposeImageTags, error) {
	result := protocol.ComposeImageTags{Repository: repo.Name(), Tags: []string{}}
	u := url.URL{Scheme: repo.Scheme(), Host: repo.RegistryStr(), Path: "/v2/" + repo.RepositoryStr() + "/tags/list"}
	query := url.Values{"n": {"500"}}
	if last != "" {
		query.Set("last", last)
	}
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return result, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return result, errors.New("registry refused tag lookup")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 768<<10+1))
	if err != nil || len(data) > 768<<10 {
		return result, errors.New("registry tag page exceeds limit")
	}
	var page struct {
		Tags []string `json:"tags"`
	}
	if json.Unmarshal(data, &page) != nil {
		return result, errors.New("invalid registry tag page")
	}
	seen := map[string]bool{}
	for _, tag := range page.Tags {
		if !registryTag.MatchString(tag) {
			return result, errors.New("invalid registry tag")
		}
		if !seen[tag] {
			seen[tag] = true
			result.Tags = append(result.Tags, tag)
		}
	}
	if link := resp.Header.Get("Link"); link != "" {
		// Distribution's next link carries the last tag. Preserve its cursor,
		// while reconstructing the next request ourselves against this repository.
		start, end := strings.Index(link, "<"), strings.Index(link, ">")
		if start < 0 || end <= start {
			return result, errors.New("invalid registry pagination")
		}
		next, err := url.Parse(link[start+1 : end])
		if err != nil {
			return result, err
		}
		result.Next = next.Query().Get("last")
		if !registryTag.MatchString(result.Next) || result.Next == last {
			return result, errors.New("invalid registry cursor")
		}
	}
	sort.Strings(result.Tags)
	return result, nil
}
