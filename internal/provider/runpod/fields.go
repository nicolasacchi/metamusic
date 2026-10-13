package runpod

// Field is one JSON path the client reads or writes in a management operation. Schema names a schema in
// the OpenAPI snapshot's components; an empty Schema means Path is a query parameter of Operation.
// TestClient_ManagementFieldsInSnapshot resolves every entry (C77.2), so a renamed field in a future
// spec fails a test instead of silently being ignored.
type Field struct{ Operation, Schema, Path string }

// ManagementOperations are the (method, path) pairs the client calls on the management host.
var ManagementOperations = []string{
	"GET /v2/serverless",
	"POST /v2/serverless",
	"GET /v2/serverless/{id}",
	"PATCH /v2/serverless/{id}",
	"GET /v2/serverless/{id}/workers",
	"GET /v2/catalog/gpus",
	"GET /v2/billing/serverless",
}

func fields(op, schema string, paths ...string) []Field {
	out := make([]Field, len(paths))
	for i, p := range paths {
		out[i] = Field{Operation: op, Schema: schema, Path: p}
	}
	return out
}

var spec = []string{"name", "type", "scaling.type", "scaling.queueDelay", "workers.min", "workers.max", "workers.idleTimeout",
	"gpu.count", "gpu.pools", "gpu.excludedTypes", "gpu.minCudaVersion", "flashboot", "timeout", "image", "env"}

// ManagementFields is the table of fields the client declares.
var ManagementFields = concat(
	fields("POST /v2/serverless", "CreateEndpointRequest", spec...),
	fields("PATCH /v2/serverless/{id}", "UpdateEndpointRequest", spec[0:1]...),
	fields("PATCH /v2/serverless/{id}", "UpdateEndpointRequest", spec[2:]...),
	fields("GET /v2/serverless/{id}", "Endpoint", "id", "name", "type", "scaling.type", "scaling.queueDelay", "scaling.requestCount",
		"workers.min", "workers.max", "workers.idleTimeout", "gpu.count", "gpu.pools", "gpu.excludedTypes", "gpu.minCudaVersion",
		"gpu.allowedCudaVersions", "flashboot", "timeout", "image", "env", "requestUrls"),
	fields("GET /v2/serverless/{id}/workers", "Worker", "id", "status", "isStale", "image", "startedAt", "uptimeSeconds", "gpuTypeId"),
	fields("GET /v2/catalog/gpus", "GpuType", "id", "name", "pool", "price.serverless"),
	fields("GET /v2/billing/serverless", "BillingRecord", "startTime", "endTime", "serverlessId", "totalAmount", "gpuAmount", "cpuAmount", "diskAmount"),
	fields("GET /v2/billing/serverless", "", "serverlessId", "bucketSize", "startTime", "endTime"),
	fields("*", "Problem", "title", "status", "detail", "errors"),
)

func concat(parts ...[]Field) []Field {
	var out []Field
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
