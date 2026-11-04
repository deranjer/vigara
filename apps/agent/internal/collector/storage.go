package collector

import (
	"context"
	"github.com/shirou/gopsutil/v4/disk"
)

type storageSource interface {
	Partitions(context.Context) ([]disk.PartitionStat, error)
	Usage(context.Context, string) (*disk.UsageStat, error)
	IOCounters(context.Context) (map[string]disk.IOCountersStat, error)
}

type nativeStorage struct{}

func (c *Collector) storageSource() storageSource {
	if c.storage != nil {
		return c.storage
	}
	return nativeStorage{}
}

func (nativeStorage) Usage(ctx context.Context, path string) (*disk.UsageStat, error) {
	return disk.UsageWithContext(ctx, path)
}
