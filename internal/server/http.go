// Package server – HTTP/HTTPS gateway for Horizon.
//
// This adds an optional REST interface that coexists with the native
// Kafka-protocol TCP server.  Clients can publish messages to any topic
// using a simple HTTP POST — no Kafka client library required.
//
// # URL scheme
//
//	POST /topics/{topic}   – produce a message (partition chosen by broker)
//	GET  /topics           – list known topics
//	GET  /topics/{topic}   – topic metadata
//	GET  /health           – liveness probe
//
// # Admin endpoints
//
//	PUT    /admin/topics/{topic}   – create a topic
//	DELETE /admin/topics/{topic}   – delete a topic and all its data
//	POST   /admin/topics/{topic}/purge  – purge all data, keep topic config
//	PATCH  /admin/topics/{topic}   – update topic configuration
//
// # Query parameters (produce)
//
//	compression  – none | gzip | snappy | lz4 | zstd (default none)
//	type         – shorthand for content type:
//	                 json      → application/json
//	                 avro      → application/vnd.apache.avro+binary
//	                 protobuf  → application/x-protobuf
//	                 msgpack   → application/x-msgpack
//	                 xml       → application/xml
//	                 string    → text/plain
//	                 binary    → application/octet-stream
//	               (or any full MIME, e.g. "application/cbor")
//	key          – record key (UTF-8 string); when present the broker
//	               hashes the key to choose a consistent partition
//
// Partition assignment is handled entirely by the broker:
//   - With key:    hash(key) % numPartitions (consistent, like Kafka)
//   - Without key: round-robin across partitions
//
// Example:
//
//	POST /topics/orders?type=json&compression=gzip&key=order-42
//
// # HTTP headers (produce – optional overrides)
//
//	Content-Type            – overrides ?type when both are present
//	X-Horizon-Key           – overrides ?key
//	X-Horizon-Compression   – overrides ?compression
//	X-Horizon-Header-*      – each matching header becomes a Kafka record
//	                          header (prefix stripped)
//
// # TLS
//
// TLS is activated automatically when both tls_cert_file and tls_key_file
// are configured.
package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"horizon/internal/broker"
	"horizon/internal/storage"
)

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

// HTTPConfig holds configuration for the HTTP/HTTPS gateway.
type HTTPConfig struct {
	// Addr is the listen address, e.g. ":8080".
	Addr string

	// TLSCertFile is the path to the TLS certificate (PEM).
	// When both TLSCertFile and TLSKeyFile are set the server uses HTTPS.
	TLSCertFile string

	// TLSKeyFile is the path to the TLS private key (PEM).
	TLSKeyFile string

	// MaxBodyBytes limits the size of a single produce request body.
	// Zero means 100 MB (same as the Kafka-protocol default).
	MaxBodyBytes int64

	// ReadTimeout for the HTTP server (default 30s).
	ReadTimeout time.Duration

	// WriteTimeout for the HTTP server (default 30s).
	WriteTimeout time.Duration
}

// DefaultHTTPConfig returns sensible defaults.
func DefaultHTTPConfig() HTTPConfig {
	return HTTPConfig{
		Addr:         ":8080",
		MaxBodyBytes: 100 * 1024 * 1024, // 100 MB
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
}

// ---------------------------------------------------------------------------
// HTTP Server
// ---------------------------------------------------------------------------

// HTTPServer is the HTTP/HTTPS gateway for Horizon.
type HTTPServer struct {
	broker *broker.Broker
	config HTTPConfig
	server *http.Server
}

// NewHTTPServer creates a new HTTP server wired to the given broker.
func NewHTTPServer(b *broker.Broker, cfg HTTPConfig) *HTTPServer {
	if cfg.MaxBodyBytes == 0 {
		cfg.MaxBodyBytes = 100 * 1024 * 1024
	}
	if cfg.ReadTimeout == 0 {
		cfg.ReadTimeout = 30 * time.Second
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = 30 * time.Second
	}

	hs := &HTTPServer{
		broker: b,
		config: cfg,
	}

	mux := http.NewServeMux()

	// Produce endpoint
	mux.HandleFunc("POST /topics/{topic}", hs.handleProduce)

	// Metadata / discovery endpoints
	mux.HandleFunc("GET /topics", hs.handleListTopics)
	mux.HandleFunc("GET /topics/{topic}", hs.handleTopicMetadata)

	// Admin endpoints
	mux.HandleFunc("PUT /admin/topics/{topic}", hs.handleCreateTopic)
	mux.HandleFunc("DELETE /admin/topics/{topic}", hs.handleDeleteTopic)
	mux.HandleFunc("POST /admin/topics/{topic}/purge", hs.handlePurgeTopic)
	mux.HandleFunc("PATCH /admin/topics/{topic}", hs.handleUpdateTopic)

	// Health check
	mux.HandleFunc("GET /health", hs.handleHealth)

	hs.server = &http.Server{
		Addr:         cfg.Addr,
		Handler:      mux,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	}

	return hs
}

// Start starts listening in a background goroutine.
func (hs *HTTPServer) Start() error {
	ln, err := net.Listen("tcp", hs.config.Addr)
	if err != nil {
		return fmt.Errorf("http: failed to listen on %s: %w", hs.config.Addr, err)
	}

	useTLS := hs.config.TLSCertFile != "" && hs.config.TLSKeyFile != ""

	go func() {
		var serveErr error
		if useTLS {
			serveErr = hs.server.ServeTLS(ln, hs.config.TLSCertFile, hs.config.TLSKeyFile)
		} else {
			serveErr = hs.server.Serve(ln)
		}
		if serveErr != nil && serveErr != http.ErrServerClosed {
			log.Printf("HTTP server error: %v", serveErr)
		}
	}()

	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	log.Printf("HTTP gateway listening on %s://%s", scheme, ln.Addr())
	return nil
}

// Stop performs a graceful shutdown with a 10-second deadline.
func (hs *HTTPServer) Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return hs.server.Shutdown(ctx)
}

// ---------------------------------------------------------------------------
// Produce handler
// ---------------------------------------------------------------------------

// produceResponse is the JSON response returned after a successful produce.
type produceResponse struct {
	Topic       string `json:"topic"`
	Partition   int32  `json:"partition"`
	Offset      int64  `json:"offset"`
	Timestamp   int64  `json:"timestamp"`
	Compression string `json:"compression"`
	ContentType string `json:"content_type"`
}

// errorResponse is the JSON payload for errors.
type errorResponse struct {
	Error string `json:"error"`
}

func (hs *HTTPServer) handleProduce(w http.ResponseWriter, r *http.Request) {
	topic := r.PathValue("topic")
	if topic == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "missing topic in URL"})
		return
	}

	q := r.URL.Query()

	// ----- Compression (header > query param > default none) --------------
	compressionName := "none"
	if hdr := r.Header.Get("X-Horizon-Compression"); hdr != "" {
		compressionName = strings.ToLower(strings.TrimSpace(hdr))
	} else if qc := q.Get("compression"); qc != "" {
		compressionName = strings.ToLower(strings.TrimSpace(qc))
	}
	if !isValidCompression(compressionName) {
		writeJSON(w, http.StatusBadRequest, errorResponse{
			Error: fmt.Sprintf("unsupported compression %q (use none|gzip|snappy|lz4|zstd)", compressionName),
		})
		return
	}

	// ----- Content / data type (Content-Type header > ?type > default) ----
	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		if qt := q.Get("type"); qt != "" {
			contentType = resolveTypeShorthand(qt)
		} else {
			contentType = "application/octet-stream"
		}
	}

	// ----- Record key (header > query param) ------------------------------
	var key []byte
	if k := r.Header.Get("X-Horizon-Key"); k != "" {
		key = []byte(k)
	} else if qk := q.Get("key"); qk != "" {
		key = []byte(qk)
	}

	// ----- Read body -------------------------------------------------------
	body, err := io.ReadAll(io.LimitReader(r.Body, hs.config.MaxBodyBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "failed to read request body"})
		return
	}
	defer r.Body.Close()

	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "empty message body"})
		return
	}

	// ----- Apply compression to the value ---------------------------------
	if compressionName != "none" {
		body, err = compressValue(compressionName, body)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{
				Error: fmt.Sprintf("compression failed: %v", err),
			})
			return
		}
	}

	// ----- Record headers --------------------------------------------------
	recordHeaders := []storage.RecordHeader{
		{Key: "content-type", Value: []byte(contentType)},
	}

	if compressionName != "none" {
		recordHeaders = append(recordHeaders, storage.RecordHeader{
			Key:   "content-encoding",
			Value: []byte(compressionName),
		})
	}

	// Forward any X-Horizon-Header-* as custom record headers.
	for name, values := range r.Header {
		if strings.HasPrefix(strings.ToLower(name), "x-horizon-header-") {
			headerKey := name[len("X-Horizon-Header-"):]
			for _, v := range values {
				recordHeaders = append(recordHeaders, storage.RecordHeader{
					Key:   headerKey,
					Value: []byte(v),
				})
			}
		}
	}

	// ----- Build record & produce ------------------------------------------
	now := time.Now()
	record := storage.Record{
		OffsetDelta:    0,
		TimestampDelta: 0,
		Key:            key,
		Value:          body,
		Headers:        recordHeaders,
	}

	// Partition is chosen by the broker (or cluster) automatically:
	//   - with key:    hash(key) % numPartitions
	//   - without key: round-robin
	var partition int32
	var baseOffset int64
	if cluster := hs.broker.GetCluster(); cluster != nil {
		// Cluster mode - use the cluster-aware interface which handles routing
		type clusterProducer interface {
			ProduceAutoPartition(topic string, key []byte, records []storage.Record) (int32, int64, error)
		}
		if cp, ok := cluster.(clusterProducer); ok {
			partition, baseOffset, err = cp.ProduceAutoPartition(topic, key, []storage.Record{record})
		} else {
			partition, baseOffset, err = hs.broker.ProduceAutoPartition(topic, key, []storage.Record{record})
		}
	} else {
		partition, baseOffset, err = hs.broker.ProduceAutoPartition(topic, key, []storage.Record{record})
	}
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "not running") {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, errorResponse{Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, produceResponse{
		Topic:       topic,
		Partition:   partition,
		Offset:      baseOffset,
		Timestamp:   now.UnixMilli(),
		Compression: compressionName,
		ContentType: contentType,
	})
}

// ---------------------------------------------------------------------------
// Metadata handlers
// ---------------------------------------------------------------------------

type topicInfo struct {
	Name          string `json:"name"`
	Partitions    int32  `json:"partitions"`
	Replication   int16  `json:"replication_factor"`
	RetentionMs   int64  `json:"retention_ms,omitempty"`
	CleanupPolicy string `json:"cleanup_policy,omitempty"`
}

func (hs *HTTPServer) handleListTopics(w http.ResponseWriter, r *http.Request) {
	topics := hs.broker.ListTopics()

	out := make([]topicInfo, 0, len(topics))
	for _, t := range topics {
		out = append(out, topicInfo{
			Name:          t.Name,
			Partitions:    t.NumPartitions,
			Replication:   t.ReplicationFactor,
			RetentionMs:   t.RetentionMs,
			CleanupPolicy: t.CleanupPolicy,
		})
	}

	writeJSON(w, http.StatusOK, out)
}

func (hs *HTTPServer) handleTopicMetadata(w http.ResponseWriter, r *http.Request) {
	topic := r.PathValue("topic")
	if topic == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "missing topic"})
		return
	}

	t := hs.broker.GetTopicConfig(topic)
	if t == nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "topic not found"})
		return
	}

	writeJSON(w, http.StatusOK, topicInfo{
		Name:          t.Name,
		Partitions:    t.NumPartitions,
		Replication:   t.ReplicationFactor,
		RetentionMs:   t.RetentionMs,
		CleanupPolicy: t.CleanupPolicy,
	})
}

// ---------------------------------------------------------------------------
// Health check
// ---------------------------------------------------------------------------

func (hs *HTTPServer) handleHealth(w http.ResponseWriter, _ *http.Request) {
	resp := map[string]interface{}{
		"status": "ok",
	}
	if cluster := hs.broker.GetCluster(); cluster != nil {
		type clusterInfo interface {
			GetClusterBrokers() []broker.BrokerInfo
			GetControllerID() int32
		}
		if ci, ok := cluster.(clusterInfo); ok {
			resp["cluster"] = map[string]interface{}{
				"mode":        "cluster",
				"brokers":     len(ci.GetClusterBrokers()),
				"controller":  ci.GetControllerID(),
			}
		}
	} else {
		resp["cluster"] = map[string]interface{}{
			"mode": "standalone",
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// ---------------------------------------------------------------------------
// Admin handlers
// ---------------------------------------------------------------------------

// createTopicRequest is the JSON body for PUT /admin/topics/{topic}.
type createTopicRequest struct {
	Partitions        int   `json:"partitions"`
	ReplicationFactor int   `json:"replication_factor"`
	RetentionMs       int64 `json:"retention_ms"`
}

// createTopicResponse is returned after a successful topic creation.
type createTopicResponse struct {
	Topic             string `json:"topic"`
	Partitions        int32  `json:"partitions"`
	ReplicationFactor int16  `json:"replication_factor"`
}

func (hs *HTTPServer) handleCreateTopic(w http.ResponseWriter, r *http.Request) {
	topic := r.PathValue("topic")
	if topic == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "missing topic in URL"})
		return
	}

	// Defaults
	numPartitions := int32(3)
	replicationFactor := int16(1)

	// Read optional JSON body
	if r.Body != nil && r.ContentLength != 0 {
		var req createTopicRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid JSON body: " + err.Error()})
			return
		}
		if req.Partitions > 0 {
			numPartitions = int32(req.Partitions)
		}
		if req.ReplicationFactor > 0 {
			replicationFactor = int16(req.ReplicationFactor)
		}
	}

	// Also accept query params: ?partitions=5&replication_factor=1
	if qp := r.URL.Query().Get("partitions"); qp != "" {
		if p, err := strconv.Atoi(qp); err == nil && p > 0 {
			numPartitions = int32(p)
		}
	}
	if qr := r.URL.Query().Get("replication_factor"); qr != "" {
		if rf, err := strconv.Atoi(qr); err == nil && rf > 0 {
			replicationFactor = int16(rf)
		}
	}

	if err := hs.broker.CreateTopic(topic, numPartitions, replicationFactor); err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "already exists") {
			status = http.StatusConflict
		}
		writeJSON(w, status, errorResponse{Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusCreated, createTopicResponse{
		Topic:             topic,
		Partitions:        numPartitions,
		ReplicationFactor: replicationFactor,
	})
}

func (hs *HTTPServer) handleDeleteTopic(w http.ResponseWriter, r *http.Request) {
	topic := r.PathValue("topic")
	if topic == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "missing topic in URL"})
		return
	}

	if err := hs.broker.DeleteTopic(topic); err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		writeJSON(w, status, errorResponse{Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status": "deleted",
		"topic":  topic,
	})
}

func (hs *HTTPServer) handlePurgeTopic(w http.ResponseWriter, r *http.Request) {
	topic := r.PathValue("topic")
	if topic == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "missing topic in URL"})
		return
	}

	if err := hs.broker.PurgeTopic(topic); err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		writeJSON(w, status, errorResponse{Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status": "purged",
		"topic":  topic,
	})
}

// updateTopicRequest is the JSON body for PATCH /admin/topics/{topic}.
type updateTopicRequest struct {
	RetentionMs   *int64  `json:"retention_ms,omitempty"`
	CleanupPolicy *string `json:"cleanup_policy,omitempty"` // "delete" or "compact"
}

func (hs *HTTPServer) handleUpdateTopic(w http.ResponseWriter, r *http.Request) {
	topic := r.PathValue("topic")
	if topic == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "missing topic in URL"})
		return
	}

	var req updateTopicRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid JSON body: " + err.Error()})
		return
	}

	if req.RetentionMs == nil && req.CleanupPolicy == nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "nothing to update: provide retention_ms and/or cleanup_policy"})
		return
	}

	if err := hs.broker.UpdateTopicConfig(topic, req.RetentionMs, req.CleanupPolicy); err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		writeJSON(w, status, errorResponse{Error: err.Error()})
		return
	}

	// Return updated config
	t := hs.broker.GetTopicConfig(topic)
	if t == nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "topic not found after update"})
		return
	}

	writeJSON(w, http.StatusOK, topicInfo{
		Name:          t.Name,
		Partitions:    t.NumPartitions,
		Replication:   t.ReplicationFactor,
		RetentionMs:   t.RetentionMs,
		CleanupPolicy: t.CleanupPolicy,
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ---------------------------------------------------------------------------
// Type shorthand resolution
// ---------------------------------------------------------------------------

// typeShorthands maps user-friendly names to MIME content types.
var typeShorthands = map[string]string{
	"json":     "application/json",
	"avro":     "application/vnd.apache.avro+binary",
	"protobuf": "application/x-protobuf",
	"proto":    "application/x-protobuf",
	"msgpack":  "application/x-msgpack",
	"xml":      "application/xml",
	"string":   "text/plain",
	"text":     "text/plain",
	"binary":   "application/octet-stream",
	"bytes":    "application/octet-stream",
	"cbor":     "application/cbor",
	"csv":      "text/csv",
	"html":     "text/html",
}

// resolveTypeShorthand converts a shorthand like "json" to "application/json".
// If the input already looks like a full MIME type (contains '/') it is returned as-is.
func resolveTypeShorthand(t string) string {
	if strings.Contains(t, "/") {
		return t // already a MIME type
	}
	if mime, ok := typeShorthands[strings.ToLower(strings.TrimSpace(t))]; ok {
		return mime
	}
	return "application/octet-stream"
}

// ---------------------------------------------------------------------------
// Compression helpers
// ---------------------------------------------------------------------------

var validCompressions = map[string]bool{
	"none": true, "gzip": true, "snappy": true, "lz4": true, "zstd": true,
}

func isValidCompression(name string) bool {
	return validCompressions[name]
}

// compressValue compresses data using the named algorithm.
// Currently gzip is fully supported via the stdlib; snappy, lz4, and zstd
// are recorded as content-encoding headers for consumer-side decompression
// but the raw bytes are stored as-is (add third-party codecs as needed).
func compressValue(algo string, data []byte) ([]byte, error) {
	switch algo {
	case "gzip":
		var buf bytes.Buffer
		gw, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
		if err != nil {
			return nil, err
		}
		if _, err := gw.Write(data); err != nil {
			return nil, err
		}
		if err := gw.Close(); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	case "snappy", "lz4", "zstd":
		// Placeholder: return raw data. Wire in github.com/klauspost/compress
		// or equivalent when needed. The content-encoding header tells
		// consumers which codec was requested.
		return data, nil
	default:
		return data, nil
	}
}
