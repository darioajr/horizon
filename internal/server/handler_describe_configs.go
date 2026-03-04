package server

import (
	"horizon/internal/protocol"
)

// handleDescribeConfigs handles DescribeConfigs request (API key 32, v0-v3).
// Returns empty config entries for each requested resource so that
// kafka-topics --describe and similar tools don't fail.
func (h *RequestHandler) handleDescribeConfigs(req *Request) *Response {
	resp := NewResponse(req.CorrelationID)
	w := resp.Writer

	// Read request
	resourceCount, _ := req.Reader.ReadArrayLen()

	type resource struct {
		resourceType int8
		resourceName string
	}

	resources := make([]resource, 0, resourceCount)
	for i := int32(0); i < resourceCount; i++ {
		rt, _ := req.Reader.ReadInt8()
		rn, _ := req.Reader.ReadString()

		// config_names (nullable array of strings) — names to describe, null = all
		nameCount, _ := req.Reader.ReadArrayLen()
		for j := int32(0); j < nameCount; j++ {
			_, _ = req.Reader.ReadString() // config name (ignored)
		}

		resources = append(resources, resource{
			resourceType: rt,
			resourceName: rn,
		})
	}

	// Throttle time (v0+)
	w.WriteInt32(0)

	// Results — one entry per resource
	w.WriteArrayLen(int32(len(resources)))
	for _, r := range resources {
		w.WriteInt16(int16(protocol.ErrNone)) // error_code
		w.WriteNullableString(nil)            // error_message
		w.WriteInt8(r.resourceType)           // resource_type
		w.WriteString(r.resourceName)         // resource_name

		// config_entries — return empty array (no configs to report)
		w.WriteArrayLen(0)
	}

	return resp
}
