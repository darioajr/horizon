// Horizon - A high-performance message streaming platform compatible with Kafka protocol
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"horizon/internal/broker"
	"horizon/internal/cluster"
	"horizon/internal/config"
	"horizon/internal/server"
	"horizon/internal/storage"
	infraS3 "horizon/internal/storage/s3"
	infraRedis "horizon/internal/storage/redis"
	infraInfinispan "horizon/internal/storage/infinispan"
)

var (
	version   = "0.1.0"
	commit    = "dev"
	buildDate = "unknown"
)

func main() {
	// Parse command line flags
	configPath := flag.String("config", "", "Path to configuration file")
	port := flag.Int("port", 9092, "Broker port")
	host := flag.String("host", "0.0.0.0", "Broker host")
	advertisedHost := flag.String("advertised-host", "", "Advertised host for clients (defaults to 'localhost' if host is 0.0.0.0)")
	dataDir := flag.String("data-dir", "./data", "Data directory")
	brokerID := flag.Int("broker-id", 0, "Broker ID")
	showVersion := flag.Bool("version", false, "Show version information")
	flag.Parse()

	if *showVersion {
		fmt.Printf("Horizon %s (commit: %s, built: %s)\n", version, commit, buildDate)
		os.Exit(0)
	}

	// Load configuration
	var cfg *config.Config
	var err error

	if *configPath != "" {
		cfg, err = config.Load(*configPath)
		if err != nil {
			log.Printf("Warning: Failed to load config file %s: %v, using defaults", *configPath, err)
			cfg = config.Default()
		}
	} else {
		cfg = config.Default()
	}

	// Apply command line overrides
	if *port != 9092 || cfg.Broker.Port == 0 {
		cfg.Broker.Port = *port
	}
	if *host != "0.0.0.0" {
		cfg.Broker.Host = *host
	}
	if *advertisedHost != "" {
		cfg.Broker.AdvertisedHost = *advertisedHost
	}
	if *dataDir != "./data" {
		cfg.Storage.DataDir = *dataDir
	}
	if *brokerID != 0 {
		cfg.Broker.ID = *brokerID
	}

	// Apply environment variable overrides (highest priority after CLI flags)
	if v := os.Getenv("HORIZON_BROKER_ID"); v != "" && *brokerID == 0 {
		if id, err := fmt.Sscanf(v, "%d", &cfg.Broker.ID); id != 1 {
			log.Printf("Warning: invalid HORIZON_BROKER_ID=%q: %v", v, err)
		}
	}
	if v := os.Getenv("HORIZON_HOST"); v != "" && *host == "0.0.0.0" {
		cfg.Broker.Host = v
	}
	if v := os.Getenv("HORIZON_PORT"); v != "" && *port == 9092 {
		if _, err := fmt.Sscanf(v, "%d", &cfg.Broker.Port); err != nil {
			log.Printf("Warning: invalid HORIZON_PORT=%q: %v", v, err)
		}
	}
	if v := os.Getenv("HORIZON_ADVERTISED_HOST"); v != "" && *advertisedHost == "" {
		cfg.Broker.AdvertisedHost = v
	}
	if v := os.Getenv("HORIZON_DATA_DIR"); v != "" && *dataDir == "./data" {
		cfg.Storage.DataDir = v
	}

	// Determine effective advertised host for logging
	effectiveAdvertisedHost := cfg.Broker.AdvertisedHost
	if effectiveAdvertisedHost == "" {
		if cfg.Broker.Host == "0.0.0.0" || cfg.Broker.Host == "" {
			effectiveAdvertisedHost = "localhost"
		} else {
			effectiveAdvertisedHost = cfg.Broker.Host
		}
	}

	log.Printf("Starting Horizon %s", version)
	log.Printf("  Broker ID: %d", cfg.Broker.ID)
	log.Printf("  Listen: %s:%d", cfg.Broker.Host, cfg.Broker.Port)
	log.Printf("  Advertised: %s:%d", effectiveAdvertisedHost, cfg.Broker.Port)
	log.Printf("  Data Dir: %s", cfg.Storage.DataDir)

	// Create broker configuration
	brokerCfg := broker.BrokerConfig{
		NodeID:                   int32(cfg.Broker.ID),
		Host:                     cfg.Broker.Host,
		AdvertisedHost:           cfg.Broker.AdvertisedHost,
		Port:                     int32(cfg.Broker.Port),
		DataDir:                  cfg.Storage.DataDir,
		SegmentBytes:             int64(cfg.Storage.SegmentSizeMB) * 1024 * 1024,
		LogRetentionMs:           int64(cfg.Storage.RetentionHours) * 60 * 60 * 1000,
		DefaultNumPartitions:     int32(cfg.Defaults.NumPartitions),
		DefaultReplicationFactor: int16(cfg.Defaults.ReplicationFactor),
		AutoCreateTopics:         true,
		AccumulatorEnabled:       cfg.Performance.AdaptiveLinger,
		MaxLingerMicros:          int64(cfg.Performance.MaxLingerMicros),
		MaxCoalesceBytes:         int64(cfg.Performance.MaxCoalesceBytes),
	}

	// Create storage engine based on configured backend
	var engine storage.StorageEngine
	switch storage.BackendType(cfg.Storage.Backend) {
	case storage.BackendS3:
		s3Cfg := infraS3.Config{
			Bucket:          cfg.Storage.S3.Bucket,
			Prefix:          cfg.Storage.S3.Prefix,
			Region:          cfg.Storage.S3.Region,
			Endpoint:        cfg.Storage.S3.Endpoint,
			AccessKey:       cfg.Storage.S3.AccessKey,
			SecretKey:       cfg.Storage.S3.SecretKey,
			SegmentMaxBytes: int64(cfg.Storage.SegmentSizeMB) * 1024 * 1024,
		}
		engine, err = infraS3.New(s3Cfg)
		if err != nil {
			log.Fatalf("Failed to create S3 storage engine: %v", err)
		}
		log.Printf("  Storage backend: S3 (bucket=%s)", s3Cfg.Bucket)

	case storage.BackendRedis:
		redisCfg := infraRedis.Config{
			Addr:            cfg.Storage.Redis.Addr,
			Password:        cfg.Storage.Redis.Password,
			DB:              cfg.Storage.Redis.DB,
			KeyPrefix:       cfg.Storage.Redis.KeyPrefix,
			SegmentMaxBytes: int64(cfg.Storage.SegmentSizeMB) * 1024 * 1024,
		}
		engine, err = infraRedis.New(redisCfg)
		if err != nil {
			log.Fatalf("Failed to create Redis storage engine: %v", err)
		}
		log.Printf("  Storage backend: Redis (addr=%s)", redisCfg.Addr)

	case storage.BackendInfinispan:
		ispnCfg := infraInfinispan.Config{
			URL:             cfg.Storage.Infinispan.URL,
			CacheName:       cfg.Storage.Infinispan.CacheName,
			Username:        cfg.Storage.Infinispan.Username,
			Password:        cfg.Storage.Infinispan.Password,
			SegmentMaxBytes: int64(cfg.Storage.SegmentSizeMB) * 1024 * 1024,
		}
		engine, err = infraInfinispan.New(ispnCfg)
		if err != nil {
			log.Fatalf("Failed to create Infinispan storage engine: %v", err)
		}
		log.Printf("  Storage backend: Infinispan (url=%s)", ispnCfg.URL)

	default:
		// Default: file-based storage (no extra setup needed; broker creates it)
		log.Printf("  Storage backend: file (dir=%s)", cfg.Storage.DataDir)
	}

	// Create broker (pass engine if non-file backend was selected)
	var b *broker.Broker
	if engine != nil {
		b, err = broker.New(brokerCfg, engine)
	} else {
		b, err = broker.New(brokerCfg)
	}
	if err != nil {
		log.Fatalf("Failed to create broker: %v", err)
	}

	// Start broker
	if err := b.Start(); err != nil {
		log.Fatalf("Failed to start broker: %v", err)
	}

	// Optionally start the cluster layer
	var clusterNode *cluster.Cluster
	if cfg.Cluster.Enabled {
		// Determine advertised host for cluster
		clusterHost := effectiveAdvertisedHost

		httpPort := int32(0)
		if cfg.HTTP.Enabled {
			httpPort = int32(cfg.HTTP.Port)
		}

		gossipInterval := time.Duration(cfg.Cluster.GossipIntervalMs) * time.Millisecond
		if gossipInterval <= 0 {
			gossipInterval = 1 * time.Second
		}
		failureThreshold := time.Duration(cfg.Cluster.FailureThresholdMs) * time.Millisecond
		if failureThreshold <= 0 {
			failureThreshold = 5 * time.Second
		}

		clusterCfg := cluster.Config{
			NodeID:            int32(cfg.Broker.ID),
			Host:              clusterHost,
			KafkaPort:         int32(cfg.Broker.Port),
			RPCPort:           int32(cfg.Cluster.RPCPort),
			HTTPPort:          httpPort,
			Seeds:             cfg.Cluster.Seeds,
			GossipInterval:    gossipInterval,
			FailureThreshold:  failureThreshold,
			ReplicationFactor: int16(cfg.Defaults.ReplicationFactor),
		}

		clusterNode = cluster.New(clusterCfg, b)
		b.SetCluster(clusterNode)

		if err := clusterNode.Start(); err != nil {
			log.Fatalf("Failed to start cluster: %v", err)
		}
		log.Printf("  Cluster: enabled (rpc_port=%d, seeds=%v)", cfg.Cluster.RPCPort, cfg.Cluster.Seeds)
	} else {
		log.Printf("  Cluster: standalone mode")
	}

	// Create and start server
	serverCfg := server.ServerConfig{
		Addr:           fmt.Sprintf("%s:%d", cfg.Broker.Host, cfg.Broker.Port),
		MaxRequestSize: 100 * 1024 * 1024,
	}

	srv := server.NewServer(b, serverCfg)
	if err := srv.Start(); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}

	log.Printf("Horizon is ready to accept connections on %s", srv.Addr())

	// Optionally start the HTTP/HTTPS gateway
	var httpSrv *server.HTTPServer
	if cfg.HTTP.Enabled {
		httpCfg := server.HTTPConfig{
			Addr:        fmt.Sprintf("%s:%d", cfg.HTTP.Host, cfg.HTTP.Port),
			TLSCertFile: cfg.HTTP.TLSCertFile,
			TLSKeyFile:  cfg.HTTP.TLSKeyFile,
		}
		httpSrv = server.NewHTTPServer(b, httpCfg)
		if err := httpSrv.Start(); err != nil {
			log.Fatalf("Failed to start HTTP gateway: %v", err)
		}
	}

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigChan

	log.Printf("Received signal %s, shutting down...", sig)

	// Graceful shutdown
	if httpSrv != nil {
		if err := httpSrv.Stop(); err != nil {
			log.Printf("Error stopping HTTP gateway: %v", err)
		}
	}

	if err := srv.Stop(); err != nil {
		log.Printf("Error stopping server: %v", err)
	}

	if clusterNode != nil {
		clusterNode.Stop()
	}

	if err := b.Stop(); err != nil {
		log.Printf("Error stopping broker: %v", err)
	}

	log.Printf("Horizon shutdown complete")
}
