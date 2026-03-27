//go:build redis || all_backends

package main

import (
	"log"

	"horizon/internal/config"
	"horizon/internal/storage"
	infraRedis "horizon/internal/storage/redis"
)

func init() {
	registerBackend(storage.BackendRedis, func(cfg *config.Config) (storage.StorageEngine, error) {
		redisCfg := infraRedis.Config{
			Addr:            cfg.Storage.Redis.Addr,
			Password:        cfg.Storage.Redis.Password,
			DB:              cfg.Storage.Redis.DB,
			KeyPrefix:       cfg.Storage.Redis.KeyPrefix,
			SegmentMaxBytes: int64(cfg.Storage.SegmentSizeMB) * 1024 * 1024,
		}
		engine, err := infraRedis.New(redisCfg)
		if err != nil {
			return nil, err
		}
		log.Printf("  Storage backend: Redis (addr=%s)", redisCfg.Addr)
		return engine, nil
	})
}
