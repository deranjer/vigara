package composedeploy

import (
	"context"
	"errors"
	"testing"
)

func TestReviewImagesReportsReplicasAndMissingServices(t *testing.T) {
	const older = "nginx@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	r := Runner{runCommand: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch args[0] + " " + args[1] {
		case "container ls":
			return []byte("a\nb\nc\nd\n"), nil
		case "container inspect":
			return []byte(`{"name":"/web-1","service":"web","state":"running","ref":"nginx:stable","id":"new"}
{"name":"/web-2","service":"web","state":"exited","ref":"` + older + `","id":"old"}
{"name":"/api-1","service":"api","ref":"nginx:stable","id":"old"}
{"name":"/worker-run","service":"worker","oneOff":"True"}`), nil
		case "image inspect":
			if args[len(args)-1] != testImage {
				return nil, errors.New("image not present")
			}
			return []byte(`{"id":"new","created":"2026-11-27T05:22:00Z","version":" 1.2.3 ","revision":"a1b2c3d"}`), nil
		default:
			t.Fatalf("unexpected command: %v", args)
			return nil, nil
		}
	}}
	review := r.reviewImages(context.Background(), ".", Plan{
		ProjectName: "test", Services: []string{"web", "api", "worker"},
		Images: map[string]string{"web": testImage, "api": older, "worker": testImage},
	})
	web := review["web"]
	if web.Version != "1.2.3" || web.Revision != "a1b2c3d" || review["api"].Version != "" || review["api"].Revision != "" {
		t.Fatalf("wrong publisher metadata: %+v", review)
	}
	if len(web.Containers) != 2 || web.Containers[0].Change != "same" || web.Containers[1].Change != "changed" || web.Containers[1].State != "exited" {
		t.Fatalf("wrong replica reviews: %+v", web.Containers)
	}
	if web.CreatedAt != "2026-11-27T05:22:00Z" || review["api"].Containers[0].Change != "unknown" || len(review["worker"].Containers) != 0 {
		t.Fatalf("wrong image metadata or status: %+v", review)
	}
}

func TestImageReviewComparesPinnedDigestWithoutLocalTarget(t *testing.T) {
	if got := imageChange(liveContainerImage{Ref: testImage}, testImage, ""); got != "same" {
		t.Fatalf("same digest: %s", got)
	}
	if got := imageChange(liveContainerImage{Ref: "nginx@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}, testImage, ""); got != "digest_changed" {
		t.Fatalf("different digest: %s", got)
	}
}
