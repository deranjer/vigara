//go:build windows

package collector

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"github.com/shirou/gopsutil/v4/disk"
	"golang.org/x/sys/windows"
)

// Enumerate mounted drive letters rather than protected recovery/system
// volumes. A denied volume must not discard measurements from other drives.
func storageDrives(ctx context.Context) ([]string, error) {
	buffer := make([]uint16, 512)
	n, err := windows.GetLogicalDriveStrings(uint32(len(buffer)), &buffer[0])
	if err != nil {
		return nil, err
	}
	if n >= uint32(len(buffer)) {
		return nil, errors.New("drive list exceeds collection limit")
	}
	var drives []string
	for offset := 0; offset < int(n) && len(drives) < maxDevices; {
		if err := ctx.Err(); err != nil {
			return drives, err
		}
		end := offset
		for end < int(n) && buffer[end] != 0 {
			end++
		}
		if end == offset {
			break
		}
		path := windows.UTF16ToString(buffer[offset:end])
		ptr, err := windows.UTF16PtrFromString(path)
		if err != nil {
			return drives, err
		}
		if windows.GetDriveType(ptr) == windows.DRIVE_FIXED {
			// DriveFS and other user-session filesystems can claim DRIVE_FIXED.
			// Native block volumes resolve to HarddiskVolume device objects;
			// cloud/redirected mounts resolve to their filesystem driver's objects.
			name, _ := windows.UTF16PtrFromString(strings.TrimRight(path, "\\/"))
			target := make([]uint16, 1024)
			if _, err := windows.QueryDosDevice(name, &target[0], uint32(len(target))); err == nil && !isLocalBlockVolume(windows.UTF16ToString(target)) {
				offset = end + 1
				continue
			}
			drives = append(drives, path)
		}
		offset = end + 1
	}
	return drives, nil
}

func isLocalBlockVolume(target string) bool {
	const prefix = `\device\harddiskvolume`
	target = strings.ToLower(target)
	if !strings.HasPrefix(target, prefix) {
		return false
	}
	number := strings.TrimPrefix(target, prefix)
	if number == "" {
		return false
	}
	for _, c := range number {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func (nativeStorage) Partitions(ctx context.Context) ([]disk.PartitionStat, error) {
	drives, err := storageDrives(ctx)
	partitions := make([]disk.PartitionStat, 0, len(drives))
	for _, path := range drives {
		root, _ := windows.UTF16PtrFromString(path)
		fs := make([]uint16, 256)
		// Filesystem metadata may be restricted even when capacity is readable.
		_ = windows.GetVolumeInformation(root, nil, 0, nil, nil, nil, &fs[0], uint32(len(fs)))
		partitions = append(partitions, disk.PartitionStat{Device: path, Mountpoint: path, Fstype: windows.UTF16ToString(fs)})
	}
	return partitions, err
}

type volumePerformance struct {
	BytesRead, BytesWritten, ReadTime, WriteTime, IdleTime int64
	ReadCount, WriteCount, QueueDepth, SplitCount          uint32
	QueryTime                                              int64
	StorageDeviceNumber                                    uint32
	StorageManagerName                                     [8]uint16
	AlignmentPadding                                       uint32
}

func readVolumeCounters(path string) (disk.IOCountersStat, error) {
	name := path[:len(path)-1] // C:\ -> C:
	device, err := windows.UTF16PtrFromString(`\\.\` + name)
	if err != nil {
		return disk.IOCountersStat{}, err
	}
	handle, err := windows.CreateFile(device, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return disk.IOCountersStat{}, err
	}
	defer windows.CloseHandle(handle)
	var performance volumePerformance
	var returned uint32
	const ioctlDiskPerformance = 0x70020
	err = windows.DeviceIoControl(handle, ioctlDiskPerformance, nil, 0, (*byte)(unsafe.Pointer(&performance)), uint32(unsafe.Sizeof(performance)), &returned, nil)
	if err != nil {
		return disk.IOCountersStat{}, err
	}
	if performance.BytesRead < 0 || performance.BytesWritten < 0 {
		return disk.IOCountersStat{}, errors.New("invalid disk byte counters")
	}
	return disk.IOCountersStat{Name: name, ReadBytes: uint64(performance.BytesRead), WriteBytes: uint64(performance.BytesWritten)}, nil
}

func (nativeStorage) IOCounters(ctx context.Context) (map[string]disk.IOCountersStat, error) {
	drives, err := storageDrives(ctx)
	var failures []error
	if err != nil {
		failures = append(failures, err)
	}
	current := map[string]disk.IOCountersStat{}
	for _, path := range drives {
		if err := ctx.Err(); err != nil {
			failures = append(failures, err)
			break
		}
		value, err := readVolumeCounters(path)
		if err != nil {
			// Some virtual volumes expose capacity but do not implement disk
			// performance counters. They provide no throughput metric, rather
			// than a fabricated zero or an error for the whole disk capability.
			if errors.Is(err, windows.ERROR_INVALID_FUNCTION) || errors.Is(err, windows.ERROR_NOT_SUPPORTED) {
				continue
			}
			failures = append(failures, fmt.Errorf("disk %s: %w", path, err))
			continue
		}
		current[value.Name] = value
	}
	return current, errors.Join(failures...)
}
