//go:build !windows

package collector

import (
	"context"
	"github.com/shirou/gopsutil/v4/disk"
)

func (nativeStorage) Partitions(ctx context.Context) ([]disk.PartitionStat, error) {
	return disk.PartitionsWithContext(ctx, false)
}

func (nativeStorage) IOCounters(ctx context.Context) (map[string]disk.IOCountersStat, error) {
	return disk.IOCountersWithContext(ctx)
}
