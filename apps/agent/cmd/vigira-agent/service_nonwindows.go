//go:build !windows

package main

import "context"

func runAsService(run func(context.Context) error) error {
	ctx, cancel := interruptContext()
	defer cancel()
	return run(ctx)
}
