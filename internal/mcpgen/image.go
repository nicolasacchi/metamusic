package mcpgen

import (
	"encoding/base64"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxImageBytes is the largest decoded image an MCP result carries (C52): the
// spectrogram budget is 100 000 bytes, so anything bigger is a server bug.
const maxImageBytes = 100000

// imageResult converts the body of an x-mm.mcp_image operation: when
// body.image has a mime_type and data_base64, the result is
// [ImageContent, TextContent(body without data_base64)] and the structured
// content is the same body, so the image bytes are not repeated as ~133 k
// characters of base64 text. It returns nil when there is no image to convert
// (e.g. inline=false), which leaves the normal result. A bad payload is an
// INTERNAL tool error; an oversized one IMAGE_TOO_LARGE.
func imageResult(body map[string]any) *mcp.CallToolResult {
	img, ok := body["image"].(map[string]any)
	if !ok {
		return nil
	}
	mime, _ := img["mime_type"].(string)
	b64, _ := img["data_base64"].(string)
	if mime == "" || b64 == "" {
		return nil
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return problemResult("INTERNAL", "the image payload is not valid base64")
	}
	if len(data) > maxImageBytes {
		return problemResult("IMAGE_TOO_LARGE", "the image exceeds the 100000 byte limit")
	}
	clean := make(map[string]any, len(body))
	for k, v := range body {
		clean[k] = v
	}
	cleanImg := make(map[string]any, len(img))
	for k, v := range img {
		if k != "data_base64" {
			cleanImg[k] = v
		}
	}
	clean["image"] = cleanImg
	text, err := json.Marshal(clean)
	if err != nil {
		return problemResult("INTERNAL", "could not render the result")
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.ImageContent{Data: data, MIMEType: mime}, &mcp.TextContent{Text: string(text)}},
		StructuredContent: clean,
	}
}
