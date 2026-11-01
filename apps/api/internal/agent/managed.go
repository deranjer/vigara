package agent

import "github.com/deranjer/vigara/apps/api/internal/backups"

func (h *Handler) ConfigureManagedBackups(manager *backups.Manager) { h.managedBackups = manager }
