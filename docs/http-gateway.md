# HTTP/HTTPS Gateway

Horizon ships an **optional HTTP/HTTPS gateway** that runs alongside the native Kafka binary-protocol TCP server. It lets any HTTP client (curl, fetch, Postman, your microservice…) publish messages without a Kafka client library.

## Enabling

Add the `http` section to your `config.yaml`:

```yaml
http:
  enabled: true
  host: "0.0.0.0"
  port: 8080
  # tls_cert_file: "/path/to/cert.pem"   # uncomment for HTTPS
  # tls_key_file:  "/path/to/key.pem"
```

When both `tls_cert_file` and `tls_key_file` are set the gateway starts as HTTPS automatically.

---

## Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/topics/{topic}` | Produce a message to the topic |
| `GET`  | `/topics` | List all known topics |
| `GET`  | `/topics/{topic}` | Get metadata for a single topic |
| `GET`  | `/health` | Liveness probe (`{"status":"ok"}`) |
| `PUT` | `/admin/topics/{topic}` | Create a new topic |
| `DELETE` | `/admin/topics/{topic}` | Delete a topic and all its data |
| `POST` | `/admin/topics/{topic}/purge` | Purge topic data (keep config) |
| `PATCH` | `/admin/topics/{topic}` | Update topic configuration |

---

## Producing Messages

The body of the `POST` request **is** the message value. The body can be any content (`application/json`, raw bytes, Avro, Protobuf, XML — anything).

### URL Query Parameters

All common settings can be expressed as **query parameters** so you don't need to repeat HTTP headers on every call:

| Param | Type | Default | Description |
|-------|------|---------|-------------|
| `compression` | string | `none` | Value compression: `none`, `gzip`, `snappy`, `lz4`, `zstd` |
| `type` | string | `binary` | Data type shorthand (see table below) or full MIME type |
| `key` | string | *(none)* | Record key (UTF-8) — used by the broker for consistent partition hashing |

#### Type Shorthands

| Shorthand | Resolved MIME |
|-----------|---------------|
| `json` | `application/json` |
| `avro` | `application/vnd.apache.avro+binary` |
| `protobuf` / `proto` | `application/x-protobuf` |
| `msgpack` | `application/x-msgpack` |
| `xml` | `application/xml` |
| `string` / `text` | `text/plain` |
| `binary` / `bytes` | `application/octet-stream` |
| `cbor` | `application/cbor` |
| `csv` | `text/csv` |

If the value contains a `/` it is treated as a full MIME type (e.g. `?type=application/cbor`).

### HTTP Headers (optional overrides)

Headers take precedence over query parameters when both are present:

| Header | Overrides | Description |
|--------|-----------|-------------|
| `Content-Type` | `?type` | Content type of the message body |
| `X-Horizon-Key` | `?key` | Record key |
| `X-Horizon-Compression` | `?compression` | Compression algorithm |
| `X-Horizon-Header-*` | — | Each header is forwarded as a Kafka record header (prefix stripped) |

### Partition Assignment

Partition selection is handled **entirely by the broker** — the HTTP client never needs to specify it:

- **With key**: `hash(key) % numPartitions` — consistent hashing, same key always goes to the same partition (ordering guarantee)
- **Without key**: round-robin across all partitions (maximum throughput)

The assigned partition is returned in the response.

### Priority Order

For each setting the resolution order is:

1. **HTTP header** (`X-Horizon-*` or `Content-Type`)
2. **Query parameter** (`?key=…`, `?type=…`, etc.)
3. **Default value**

---

## Examples

### Basic JSON message

```bash
curl -X POST "http://localhost:8080/topics/orders?type=json" \
  -d '{"orderId": 42, "amount": 99.90}'
```

### Avro with gzip and key

```bash
curl -X POST "http://localhost:8080/topics/events?type=avro&compression=gzip&key=evt-123" \
  --data-binary @event.avro
```

### Protobuf

```bash
curl -X POST "http://localhost:8080/topics/telemetry?type=protobuf&compression=zstd" \
  --data-binary @payload.pb
```

### Plain text string

```bash
curl -X POST "http://localhost:8080/topics/logs?type=string" \
  -d "2026-03-01 12:00:00 INFO Application started"
```

### Custom record headers

```bash
curl -X POST "http://localhost:8080/topics/orders?type=json&key=order-99" \
  -H "X-Horizon-Header-source: checkout-service" \
  -H "X-Horizon-Header-trace-id: abc-123-def" \
  -d '{"orderId": 99}'
```

### Using HTTP headers instead of query params

```bash
curl -X POST "http://localhost:8080/topics/orders" \
  -H "Content-Type: application/json" \
  -H "X-Horizon-Key: order-42" \
  -H "X-Horizon-Compression: gzip" \
  -d '{"orderId": 42}'
```

---

## Response

A successful produce returns HTTP 200 with JSON:

```json
{
  "topic": "orders",
  "partition": 1,
  "offset": 0,
  "timestamp": 1709294400000,
  "compression": "gzip",
  "content_type": "application/json"
}
```

Errors return an appropriate HTTP status (400, 404, 500, 503) with:

```json
{
  "error": "description of the problem"
}
```

---

## Record Storage Details

Every message produced via HTTP is stored as a standard Kafka-compatible record. The HTTP metadata is preserved as **Kafka record headers**:

| Record Header | Value | Always Present |
|---------------|-------|:-:|
| `content-type` | Resolved MIME type (e.g. `application/json`) | Yes |
| `content-encoding` | Compression algorithm (e.g. `gzip`) | Only when compression ≠ `none` |
| *(custom)* | Any `X-Horizon-Header-*` values | Only when sent |

This means Kafka consumers (Java, Python, Go…) can read `content-type` and `content-encoding` from the record headers to deserialise/decompress the value correctly — regardless of whether the message was produced via HTTP or the native Kafka protocol.

---

## Admin API

The admin endpoints allow you to manage topics without a Kafka client.

### Create Topic

```bash
# Create topic with 6 partitions
curl -X PUT "http://localhost:8080/admin/topics/orders?partitions=6"

# Create with default partition count
curl -X PUT "http://localhost:8080/admin/topics/events"
```

**Query Parameters:**

| Param | Type | Default | Description |
|-------|------|---------|-------------|
| `partitions` | int | `defaults.num_partitions` from config | Number of partitions to create |

**Response (201 Created):**

```json
{
  "topic": "orders",
  "partitions": 6
}
```

Returns `409 Conflict` if the topic already exists.

### Delete Topic

```bash
curl -X DELETE "http://localhost:8080/admin/topics/old-events"
```

**Response (200 OK):**

```json
{
  "deleted": "old-events"
}
```

Returns `404 Not Found` if the topic does not exist.

### Purge Topic

Deletes all data for a topic but keeps the topic and its partition count.

```bash
curl -X POST "http://localhost:8080/admin/topics/events/purge"
```

**Response (200 OK):**

```json
{
  "purged": "events"
}
```

Returns `404 Not Found` if the topic does not exist.

### Update Topic Configuration

Patch mutable topic settings. Only provided fields are updated.

```bash
# Update retention to 48 hours
curl -X PATCH "http://localhost:8080/admin/topics/orders" \
  -H "Content-Type: application/json" \
  -d '{"retention_ms": 172800000}'

# Update cleanup policy
curl -X PATCH "http://localhost:8080/admin/topics/orders" \
  -H "Content-Type: application/json" \
  -d '{"cleanup_policy": "compact"}'
```

**Request Body (JSON):**

| Field | Type | Description |
|-------|------|-------------|
| `retention_ms` | int64 | Retention period in milliseconds |
| `cleanup_policy` | string | `"delete"` or `"compact"` |

**Response (200 OK):**

```json
{
  "topic": "orders",
  "updated": true
}
```

Returns `404 Not Found` if the topic does not exist.

---

## Docker

The Dockerfile exposes both ports:

```dockerfile
EXPOSE 9092 8080
```

Docker Compose example:

```yaml
services:
  horizon:
    image: horizon:latest
    ports:
      - "9092:9092"   # Kafka protocol
      - "8080:8080"   # HTTP gateway
    volumes:
      - ./configs/config.yaml:/app/config.yaml
      - horizon-data:/data
```
