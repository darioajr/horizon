//go:build infinispan || all_backends

package main

import (
	"log"

	"horizon/internal/config"
	"horizon/internal/storage"
	infraInfinispan "horizon/internal/storage/infinispan"
)

func init() {
	registerBackend(storage.BackendInfinispan, func(cfg *config.Config) (storage.StorageEngine, error) {
		ispnCfg := infraInfinispan.Config{
			URL:             cfg.Storage.Infinispan.URL,
			CacheName:       cfg.Storage.Infinispan.CacheName,
			Username:        cfg.Storage.Infinispan.Username,
			Password:        cfg.Storage.Infinispan.Password,
			SegmentMaxBytes: int64(cfg.Storage.SegmentSizeMB) * 1024 * 1024,
		}
		engine, err := infraInfinispan.New(ispnCfg)
		if err != nil {
			return nil, err
		}
		log.Printf("  Storage backend: Infinispan (url=%s)", ispnCfg.URL)
		return engine, nil
	})
}
