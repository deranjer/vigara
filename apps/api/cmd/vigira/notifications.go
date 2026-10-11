package main

import (
	"context"

	"github.com/deranjer/vigara/apps/api/internal/notifications"
	"github.com/deranjer/vigara/apps/api/internal/notifytransport"
)

func buildNotificationDestinations(targets []notifytransport.Target) []notifications.Destination {
	destinations := make([]notifications.Destination, 0, len(targets))
	for _, target := range targets {
		destinations = append(destinations, notifications.Destination{
			ID: target.ID,
			Send: func(ctx context.Context, message notifications.Message) error {
				return target.Send(ctx, notifytransport.Message{
					EventID: message.EventID, Transition: message.Transition,
					DeviceName: message.DeviceName, RuleName: message.RuleName,
					Summary: message.Summary, OccurredAt: message.OccurredAt,
				})
			},
		})
	}
	return destinations
}
