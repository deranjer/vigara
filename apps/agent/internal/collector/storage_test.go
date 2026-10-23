package collector

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
)

type partialStorage struct{ read uint64 }

func (*partialStorage) Partitions(context.Context) ([]disk.PartitionStat, error) {
	return []disk.PartitionStat{{Mountpoint: "readable", Fstype: "test"}, {Mountpoint: "denied"}}, errors.New("another volume is unavailable")
}
func (*partialStorage) Usage(_ context.Context, path string) (*disk.UsageStat, error) {
	if path == "denied" {
		return nil, errors.New("access is denied")
	}
	return &disk.UsageStat{Total: 100, Free: 25}, nil
}
func (s *partialStorage) IOCounters(context.Context) (map[string]disk.IOCountersStat, error) {
	s.read += 100
	return map[string]disk.IOCountersStat{"readable": {ReadBytes: s.read, WriteBytes: s.read}}, fmt.Errorf("disk denied: access is denied")
}

func TestPartialStorageErrorsKeepReadableFilesystemMetrics(t *testing.T) {
	c := &Collector{storage: &partialStorage{}}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	snapshot := c.Collect(ctx)
	total, free, denied := false, false, false
	for _, metric := range snapshot.Metrics {
		if metric.Labels["mount_path"] == "readable" {
			total = total || metric.Name == "filesystem_total_bytes" && metric.Value == 100
			free = free || metric.Name == "filesystem_free_bytes" && metric.Value == 25
		}
		if metric.Labels["mount_path"] == "denied" {
			t.Fatal("fabricated capacity for inaccessible drive")
		}
	}
	for _, issue := range snapshot.Errors {
		if issue.Capability == "filesystem_capacity" && issue.Message == "volume denied: access is denied" {
			denied = true
		}
	}
	if !total || !free || !denied {
		t.Fatalf("readable capacity or drive-specific error missing: total=%v free=%v denied=%v", total, free, denied)
	}
}

func TestPartialStorageErrorsKeepReadableDiskRates(t *testing.T) {
	c := &Collector{storage: &partialStorage{}}
	var values []float64
	errorsSeen := 0
	add := func(_, _ string, value float64, _ string, labels map[string]string) {
		if labels["device"] != "readable" {
			t.Fatal("unexpected drive")
		}
		values = append(values, value)
	}
	fail := func(capability string, err error) {
		if capability != "disk_throughput" {
			t.Fatal(capability)
		}
		errorsSeen++
	}
	now := time.Now()
	c.collectDiskRates(context.Background(), now, add, fail)
	if len(values) != 0 {
		t.Fatal("rate emitted without baseline")
	}
	c.collectDiskRates(context.Background(), now.Add(time.Second), add, fail)
	if len(values) != 2 || values[0] != 100 || values[1] != 100 || errorsSeen != 2 {
		t.Fatalf("partial rates=%v errors=%d", values, errorsSeen)
	}
}
