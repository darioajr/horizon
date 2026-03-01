package storage

import (
	"fmt"
)

// BackendType identifies a storage backend.
type BackendType string

const (
	BackendFile      BackendType = "file"
	BackendS3        BackendType = "s3"
	BackendRedis     BackendType = "redis"
	BackendInfinispan BackendType = "infinispan"
)

// NewEngine creates a StorageEngine for the given backend type.
//
// The caller must supply the backend-specific configuration through one of
// the With…Config option functions.  If no backend option is given the
// default file-based engine is created using the supplied LogConfig.
//
// Usage:
//
//	engine, err := storage.NewEngine(storage.BackendFile,
//	    storage.WithFileConfig(storage.LogConfig{Dir: "./data"}))
//
//	engine, err := storage.NewEngine(storage.BackendS3,
//	    storage.WithS3Config(s3.Config{Bucket: "my-bucket"}))
func NewEngine(backend BackendType, opts ...EngineOption) (StorageEngine, error) {
	o := &engineOptions{}
	for _, fn := range opts {
		fn(o)
	}

	switch backend {
	case BackendFile, "":
		return NewLog(o.fileConfig)

	case BackendS3:
		if o.s3Factory == nil {
			return nil, fmt.Errorf("s3 backend selected but no S3 config provided; use storage.WithS3Config()")
		}
		return o.s3Factory()

	case BackendRedis:
		if o.redisFactory == nil {
			return nil, fmt.Errorf("redis backend selected but no Redis config provided; use storage.WithRedisConfig()")
		}
		return o.redisFactory()

	case BackendInfinispan:
		if o.infinispanFactory == nil {
			return nil, fmt.Errorf("infinispan backend selected but no Infinispan config provided; use storage.WithInfinispanConfig()")
		}
		return o.infinispanFactory()

	default:
		return nil, fmt.Errorf("unknown storage backend: %q", backend)
	}
}

// EngineOption configures the engine factory.
type EngineOption func(*engineOptions)

type engineOptions struct {
	fileConfig        LogConfig
	s3Factory         func() (StorageEngine, error)
	redisFactory      func() (StorageEngine, error)
	infinispanFactory func() (StorageEngine, error)
}

// WithFileConfig sets the configuration for the file-based backend.
func WithFileConfig(cfg LogConfig) EngineOption {
	return func(o *engineOptions) {
		o.fileConfig = cfg
	}
}

// WithS3Factory registers a factory for the S3 backend.
// This avoids importing the s3 sub-package from the storage package.
func WithS3Factory(factory func() (StorageEngine, error)) EngineOption {
	return func(o *engineOptions) {
		o.s3Factory = factory
	}
}

// WithRedisFactory registers a factory for the Redis backend.
func WithRedisFactory(factory func() (StorageEngine, error)) EngineOption {
	return func(o *engineOptions) {
		o.redisFactory = factory
	}
}

// WithInfinispanFactory registers a factory for the Infinispan backend.
func WithInfinispanFactory(factory func() (StorageEngine, error)) EngineOption {
	return func(o *engineOptions) {
		o.infinispanFactory = factory
	}
}
