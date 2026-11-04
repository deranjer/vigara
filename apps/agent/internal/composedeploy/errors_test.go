package composedeploy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/deranjer/vigara/internal/protocol"
	"github.com/deranjer/vigara/internal/stack"
)

func TestValidationFailureExposesOnlyKnownMessages(t *testing.T) {
	for _, known := range []error{errTarget, errCompose, errJournal, errExternalNetwork, errExternalVolume, errBindMount, errUnsupportedStack, stack.ErrInvalid} {
		err := validationFailure(fmt.Errorf("secret environment value: %w", known))
		var channelErr *protocol.ChannelError
		if !errors.As(err, &channelErr) || channelErr.Message != known.Error() || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe or missing validation diagnostic: %v", err)
		}
	}
	if validationFailure(nil) != nil {
		t.Fatal("success became an error")
	}
	if validationFailure(context.Canceled) != context.Canceled {
		t.Fatal("lost cancellation")
	}
	unknown := errors.New("secret host details")
	if validationFailure(unknown) != unknown {
		t.Fatal("unknown errors must remain subject to channel sanitization")
	}
}
