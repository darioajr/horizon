//go:build s3 || all_backends

package main

import (
	"log"

	"horizon/internal/config"
	"horizon/internal/storage"
	infraS3 "horizon/internal/storage/s3"
)

func init() {
	registerBackend(storage.BackendS3, func(cfg *config.Config) (storage.StorageEngine, error) {
		s3Cfg := infraS3.Config{
			Bucket:          cfg.Storage.S3.Bucket,
			Prefix:          cfg.Storage.S3.Prefix,
			Region:          cfg.Storage.S3.Region,
			Endpoint:        cfg.Storage.S3.Endpoint,
			AccessKey:       cfg.Storage.S3.AccessKey,
			SecretKey:       cfg.Storage.S3.SecretKey,
			SegmentMaxBytes: int64(cfg.Storage.SegmentSizeMB) * 1024 * 1024,
		}
		engine, err := infraS3.New(s3Cfg)
		if err != nil {
			return nil, err
		}
		log.Printf("  Storage backend: S3 (bucket=%s)", s3Cfg.Bucket)
		return engine, nil
	})
}
