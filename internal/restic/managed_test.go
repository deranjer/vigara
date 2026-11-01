package restic

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitializeOnlyOnMissingRepository(t *testing.T) {
	for _, code := range []int{1, 10, 11, 12, 130} {
		fake := &fakeProcess{outputs: [][]byte{[]byte("restic 0.19.1")}, errors: []error{nil, exitError(code)}}
		err := testRunner(fake).InitializeMissing(context.Background(), filepath.Join(t.TempDir(), "repository"))
		if code == 10 {
			if err != nil || len(fake.commands) != 3 || fake.commands[2][3] != "init" {
				t.Fatalf("missing repository not initialized: %v %#v", err, fake.commands)
			}
		} else if err == nil || len(fake.commands) != 2 {
			t.Fatalf("exit %d triggered initialization", code)
		}
	}
	fake := &fakeProcess{outputs: [][]byte{[]byte("restic 0.19.1"), []byte("{}")}}
	if err := testRunner(fake).InitializeMissing(context.Background(), "/repository"); err != nil || len(fake.commands) != 2 {
		t.Fatal("existing repository changed", err)
	}
}

func TestBrowseDirectoryFiltersTreeAndRejectsTraversal(t *testing.T) {
	fake := &fakeProcess{outputs: [][]byte{[]byte("restic 0.19.1"), []byte("{\"type\":\"dir\",\"path\":\"/data\",\"name\":\"data\"}\n{\"type\":\"file\",\"path\":\"/data/a\",\"name\":\"a\"}\n{\"type\":\"file\",\"path\":\"/data/nested/b\",\"name\":\"b\"}\n")}}
	entries, err := testRunner(fake).BrowseDirectory(context.Background(), "/repo", strings.Repeat("a", 64), "/data")
	if err != nil || len(entries) != 1 || entries[0].Name != "a" {
		t.Fatalf("directory listing %#v %v", entries, err)
	}
	for _, p := range []string{"relative", "/data/../secrets", "/data/", "/bad\x00"} {
		if _, err = snapshotPath(p); err == nil {
			t.Fatalf("accepted path %q", p)
		}
	}
}

func TestDownloadLimitNeverTruncatesSuccessfully(t *testing.T) {
	var sink bytes.Buffer
	w := &limitedWriter{writer: &sink, remaining: 3}
	if _, err := w.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("d")); err == nil {
		t.Fatal("download limit ignored")
	}
	if sink.String() != "abc" {
		t.Fatal("unexpected data")
	}
	fake := &fakeProcess{outputs: [][]byte{[]byte("restic 0.19.1")}, errors: []error{nil, errors.New("secret")}}
	if err := testRunner(fake).InitializeMissing(context.Background(), "/repo"); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("unsafe initialization error", err)
	}
}
