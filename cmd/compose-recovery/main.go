package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/deranjer/vigara/internal/composerecovery"
)

func main() {
	repo := flag.String("repo", "", "local checked-out portable configuration repository")
	profile := flag.String("profile", "", "profile ID to plan for")
	snapshotFile := flag.String("snapshot-file", "", "optional snapshot metadata JSON file")
	releases := flag.String("release", "", "optional comma-separated release IDs to include")
	flag.Parse()
	if *repo == "" || *profile == "" || flag.NArg() != 0 {
		fail()
	}
	var snapshot *composerecovery.SnapshotMetadata
	if *snapshotFile != "" {
		value, err := composerecovery.ReadSnapshotMetadata(*snapshotFile)
		if err != nil {
			fail()
		}
		snapshot = &value
	}
	var releaseIDs []string
	if strings.TrimSpace(*releases) != "" {
		for _, id := range strings.Split(*releases, ",") {
			releaseIDs = append(releaseIDs, strings.TrimSpace(id))
		}
	}
	plan, err := composerecovery.Build(context.Background(), *repo, *profile, snapshot, releaseIDs)
	if err != nil {
		fail()
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if enc.Encode(plan) != nil {
		fail()
	}
}

func fail() {
	fmt.Fprintln(os.Stderr, "compose-recovery: invalid input or output error; details omitted")
	os.Exit(2)
}
