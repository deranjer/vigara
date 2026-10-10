//go:build windows

package main

import (
	"context"
	"fmt"

	"golang.org/x/sys/windows/svc"
)

func runAsService(run func(context.Context) error) error {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return fmt.Errorf("detect Windows service mode: %w", err)
	}
	if !isService {
		ctx, cancel := interruptContext()
		defer cancel()
		return run(ctx)
	}
	return svc.Run("VigiraAgent", serviceRunner{run: run})
}

type serviceRunner struct{ run func(context.Context) error }

func (s serviceRunner) Execute(_ []string, requests <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.run(ctx) }()
	changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	var exitCode uint32
loop:
	for {
		select {
		case request := <-requests:
			switch request.Cmd {
			case svc.Stop, svc.Shutdown:
				changes <- svc.Status{State: svc.StopPending}
				cancel()
				if err := <-done; err != nil {
					exitCode = 1
				}
				break loop
			case svc.Interrogate:
				changes <- request.CurrentStatus
			}
		case err := <-done:
			if err != nil {
				exitCode = 1
			}
			break loop
		}
	}
	cancel()
	changes <- svc.Status{State: svc.Stopped, Win32ExitCode: exitCode}
	return false, exitCode
}
