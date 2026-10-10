// Package modules defines contracts for built-in collectors and providers.
package modules

import "context"

type Descriptor struct {
	ID           string
	Version      string
	Platforms    []string
	Capabilities []string
}

type Module interface {
	Descriptor() Descriptor
	Collect(context.Context) error
}
