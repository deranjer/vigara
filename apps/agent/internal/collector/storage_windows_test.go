//go:build windows

package collector

import (
	"context"
	"testing"
	"time"
)

func TestWindowsStorageDiagnostics(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	source := nativeStorage{}
	partitions, err := source.Partitions(ctx)
	t.Logf("Mounted filesystem count: %d; enumeration error: %v", len(partitions), err)
	for _, part := range partitions {
		usage, err := source.Usage(ctx, part.Mountpoint)
		if err != nil {
			t.Logf("Capacity unavailable for %s: %v", part.Mountpoint, err)
		} else {
			t.Logf("Capacity readable for %s (%s): %d bytes", part.Mountpoint, part.Fstype, usage.Total)
		}
	}
	counters, err := source.IOCounters(ctx)
	t.Logf("Readable disk counters: %d; remaining errors: %v", len(counters), err)
	if len(partitions) == 0 {
		t.Fatal("No mounted local filesystems enumerated")
	}
}

func TestOnlyNativeBlockVolumesAreMonitored(t *testing.T) {
	for _, tc := range []struct {
		target string
		want   bool
	}{
		{`\Device\HarddiskVolume2`, true},
		{`\device\harddiskvolume11`, true},
		{`\Device\Volume{f5ae2bcb-da00-3bf2-8935-408102040811}`, false},
		{`\Device\GoogleDriveFS`, false},
		{`\Device\LanmanRedirector\server\share`, false},
		{`\Device\HarddiskVolumeShadowCopy1`, false},
		{`\??\C:\alias`, false},
	} {
		if got := isLocalBlockVolume(tc.target); got != tc.want {
			t.Errorf("classification for %s = %v", tc.target, got)
		}
	}
}
