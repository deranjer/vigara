package selfupdate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// Operation protects agent work from replacement. Installations without the
// updater keep working normally; once provisioned, lock failures fail closed.
func Operation(ctx context.Context) (func(), error) {
	return acquire(ctx, filepath.Join(Directory, "operations.lock"), false, true)
}

func acquire(ctx context.Context, path string, exclusive, optional bool) (func(), error) {
	flags := os.O_RDONLY
	if exclusive {
		flags = os.O_RDWR
	}
	f, err := os.OpenFile(path, flags, 0)
	if optional && errors.Is(err, os.ErrNotExist) {
		return func() {}, nil
	}
	if err != nil {
		return nil, err
	}
	for {
		err = tryLock(f, exclusive)
		if err == nil {
			return func() { unlock(f); f.Close() }, nil
		}
		if !lockBusy(err) {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
