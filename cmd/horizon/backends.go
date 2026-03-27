package main

import (
	"horizon/internal/config"
	"horizon/internal/storage"
)

// backendFactory creates a StorageEngine from the application config.
type backendFactory func(cfg *config.Config) (storage.StorageEngine, error)

// backendRegistry maps backend types to their factory functions.
// Populated at init-time by build-tag-gated files (backend_*.go).
var backendRegistry = map[storage.BackendType]backendFactory{}

// registerBackend is called from init() in each backend_*.go file.
func registerBackend(bt storage.BackendType, f backendFactory) {
	backendRegistry[bt] = f
}
