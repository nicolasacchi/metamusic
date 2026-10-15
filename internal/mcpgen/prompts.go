package mcpgen

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nicolasacchi/metamusic/internal/prompts"
)

// addPrompts registers prompts.Catalog. prompts/get dispatches get_prompt through the gated handler (C112.4);
// nothing is rendered here.
func (g *gen) addPrompts(srv *mcp.Server) {
	for _, spec := range prompts.Catalog {
		p := &mcp.Prompt{Name: spec.Name, Title: spec.Title, Description: spec.Description}
		for _, a := range spec.Args {
			p.Arguments = append(p.Arguments, &mcp.PromptArgument{Name: a.Name, Description: a.Description, Required: a.Required})
		}
		srv.AddPrompt(p, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return g.getPrompt(ctx, req, spec)
		})
	}
}

type promptView struct {
	Description string `json:"description"`
	Messages    []struct {
		Role    string `json:"role"`
		Content struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Resource *struct {
				URI      string `json:"uri"`
				MIMEType string `json:"mime_type"`
				Text     string `json:"text"`
			} `json:"resource"`
		} `json:"content"`
	} `json:"messages"`
}

func (g *gen) getPrompt(ctx context.Context, req *mcp.GetPromptRequest, spec prompts.Spec) (*mcp.GetPromptResult, error) {
	args := map[string]any{"name": spec.Name}
	for _, a := range spec.Args { // only declared arguments reach the operation
		if v := req.Params.Arguments[a.Name]; v != "" {
			args[a.Name] = v
		}
	}
	_, body, err := g.present(ctx, req.Extra, req.Session, "get_prompt", args)
	if err != nil {
		return nil, err
	}
	var v promptView
	if json.Unmarshal(body, &v) != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "INTERNAL"}
	}
	res := &mcp.GetPromptResult{Description: v.Description}
	for _, m := range v.Messages {
		pm := &mcp.PromptMessage{Role: mcp.Role(m.Role)}
		if m.Content.Type == "resource" && m.Content.Resource != nil {
			r := m.Content.Resource
			pm.Content = &mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: r.URI, MIMEType: r.MIMEType, Text: r.Text}}
		} else {
			pm.Content = &mcp.TextContent{Text: m.Content.Text}
		}
		res.Messages = append(res.Messages, pm)
	}
	return res, nil
}
