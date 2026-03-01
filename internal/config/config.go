package config

import (
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config represents the complete Horizon configuration
type Config struct {
	Broker      BrokerConfig      `yaml:"broker"`
	Cluster     ClusterConfig     `yaml:"cluster"`
	HTTP        HTTPConfig        `yaml:"http"`
	Storage     StorageConfig     `yaml:"storage"`
	Defaults    DefaultsConfig    `yaml:"defaults"`
	Performance PerformanceConfig `yaml:"performance"`
}

// HTTPConfig holds settings for the optional HTTP/HTTPS gateway.
type HTTPConfig struct {
	Enabled     bool   `yaml:"enabled"`
	Host        string `yaml:"host"`
	Port        int    `yaml:"port"`
	TLSCertFile string `yaml:"tls_cert_file"`
	TLSKeyFile  string `yaml:"tls_key_file"`
}

// ClusterConfig holds settings for the cluster mode.
type ClusterConfig struct {
	Enabled            bool     `yaml:"enabled"`           // enable cluster mode (default: false = standalone)
	RPCPort            int      `yaml:"rpc_port"`          // inter-broker RPC / gossip port
	Seeds              []string `yaml:"seeds"`             // bootstrap peers ("host:port")
	GossipIntervalMs   int      `yaml:"gossip_interval_ms"`   // gossip heartbeat interval (default 1000)
	FailureThresholdMs int      `yaml:"failure_threshold_ms"` // time before marking a node dead (default 5000)
}

// BrokerConfig contains broker-specific settings
type BrokerConfig struct {
	ID             int    `yaml:"id"`
	Host           string `yaml:"host"`
	Port           int    `yaml:"port"`
	ClusterID      string `yaml:"cluster_id"`
	AdvertisedHost string `yaml:"advertised_host"` // Host to advertise to clients (defaults to Host if empty)
}

// StorageConfig contains storage-specific settings
type StorageConfig struct {
	// Backend selects the storage engine: "file" (default), "s3", "redis", "infinispan".
	Backend        string        `yaml:"backend"`
	DataDir        string        `yaml:"data_dir"`
	SegmentSizeMB  int           `yaml:"segment_size_mb"`
	RetentionHours int           `yaml:"retention_hours"`
	SyncWrites     bool          `yaml:"sync_writes"`
	FlushInterval  time.Duration `yaml:"flush_interval"`

	// Backend-specific configuration (only the selected backend needs to be set)
	S3         S3Config         `yaml:"s3"`
	Redis      RedisConfig      `yaml:"redis"`
	Infinispan InfinispanConfig `yaml:"infinispan"`
}

// S3Config contains settings for the S3 storage backend.
type S3Config struct {
	Bucket    string `yaml:"bucket"`
	Prefix    string `yaml:"prefix"`
	Region    string `yaml:"region"`
	Endpoint  string `yaml:"endpoint"`
	AccessKey string `yaml:"access_key"`
	SecretKey string `yaml:"secret_key"`
}

// RedisConfig contains settings for the Redis storage backend.
type RedisConfig struct {
	Addr      string `yaml:"addr"`
	Password  string `yaml:"password"`
	DB        int    `yaml:"db"`
	KeyPrefix string `yaml:"key_prefix"`
}

// InfinispanConfig contains settings for the Infinispan storage backend.
type InfinispanConfig struct {
	URL       string `yaml:"url"`
	CacheName string `yaml:"cache_name"`
	Username  string `yaml:"username"`
	Password  string `yaml:"password"`
}

// DefaultsConfig contains default topic settings
type DefaultsConfig struct {
	NumPartitions     int `yaml:"num_partitions"`
	ReplicationFactor int `yaml:"replication_factor"`
}

// PerformanceConfig contains performance tuning settings
type PerformanceConfig struct {
	WriteBufferKB  int `yaml:"write_buffer_kb"`
	MaxConnections int `yaml:"max_connections"`
	IOThreads      int `yaml:"io_threads"`
	ReadBufferSize int `yaml:"read_buffer_size"`
}

// Default returns the default configuration
func Default() *Config {
	return &Config{
		Broker: BrokerConfig{
			ID:        1,
			Host:      "0.0.0.0",
			Port:      9092,
			ClusterID: "horizon-cluster",
		},
		Cluster: ClusterConfig{
			Enabled:            false,
			RPCPort:            9093,
			Seeds:              nil,
			GossipIntervalMs:   1000,
			FailureThresholdMs: 5000,
		},
		HTTP: HTTPConfig{
			Enabled: false,
			Host:    "0.0.0.0",
			Port:    8080,
		},
		Storage: StorageConfig{
			Backend:        "file",
			DataDir:        "./data",
			SegmentSizeMB:  1024,
			RetentionHours: 168, // 7 days
			SyncWrites:     false,
			FlushInterval:  time.Second,
		},
		Defaults: DefaultsConfig{
			NumPartitions:     3,
			ReplicationFactor: 1,
		},
		Performance: PerformanceConfig{
			WriteBufferKB:  2048,
			MaxConnections: 10000,
			IOThreads:      4,
			ReadBufferSize: 65536,
		},
	}
}

// Load reads configuration from a YAML file
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	cfg := Default()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// Save writes configuration to a YAML file
func (c *Config) Save(path string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

// Validate checks if the configuration is valid
func (c *Config) Validate() error {
	if c.Broker.Port < 1 || c.Broker.Port > 65535 {
		return ErrInvalidPort
	}
	if c.Storage.SegmentSizeMB < 1 {
		return ErrInvalidSegmentSize
	}
	if c.Defaults.NumPartitions < 1 {
		return ErrInvalidPartitions
	}
	return nil
}
