package api_test

import (
	"context"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nicolasacchi/metamusic/internal/api"
)

type imgBody struct {
	Name  string         `json:"name"`
	Image *api.ImageData `json:"image,omitempty"`
}
type imgOut struct{ Body imgBody }
type imgOutNoField struct {
	Body struct {
		Name string `json:"name"`
	}
}
type imgOutWrongType struct {
	Body struct {
		Image *string `json:"image"`
	}
}
type imgOutValue struct {
	Body struct {
		Image api.ImageData `json:"image"`
	}
}
type imgOutNoBody struct{ Name string }

func mustPanic(t *testing.T, name string, f func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s: want a panic", name)
		}
	}()
	f()
}

func TestRegister_MCPImageValidation(t *testing.T) {
	t.Parallel()
	imgMeta := func() api.Meta { m := goodMeta(); m.MCPImage = true; return m }
	post := func() huma.Operation { o := goodOp(); o.Method = "POST"; return o }

	mustPanic(t, "POST", func() {
		api.Register(newAPI(), post(), imgMeta(), func(context.Context, *nothing) (*imgOut, error) { return &imgOut{}, nil })
	})
	mustPanic(t, "exclude", func() {
		m := imgMeta()
		m.MCP = "exclude:binary"
		api.Register(newAPI(), goodOp(), m, func(context.Context, *nothing) (*imgOut, error) { return &imgOut{}, nil })
	})
	mustPanic(t, "no image field", func() {
		api.Register(newAPI(), goodOp(), imgMeta(), func(context.Context, *nothing) (*imgOutNoField, error) { return &imgOutNoField{}, nil })
	})
	mustPanic(t, "wrong image type", func() {
		api.Register(newAPI(), goodOp(), imgMeta(), func(context.Context, *nothing) (*imgOutWrongType, error) { return &imgOutWrongType{}, nil })
	})
	mustPanic(t, "value instead of pointer", func() {
		api.Register(newAPI(), goodOp(), imgMeta(), func(context.Context, *nothing) (*imgOutValue, error) { return &imgOutValue{}, nil })
	})
	mustPanic(t, "no body", func() {
		api.Register(newAPI(), goodOp(), imgMeta(), func(context.Context, *nothing) (*imgOutNoBody, error) { return &imgOutNoBody{}, nil })
	})
	// the valid shape registers
	api.Register(newAPI(), goodOp(), imgMeta(), func(context.Context, *nothing) (*imgOut, error) { return &imgOut{}, nil })
}

func TestRegister_MCPImageShape(t *testing.T) {
	t.Parallel()
	a := newAPI()
	m := goodMeta()
	m.MCPImage = true
	api.Register(a, goodOp(), m, func(context.Context, *nothing) (*imgOut, error) { return &imgOut{}, nil })
	op := a.OpenAPI().Paths["/api/thing"].Get
	if got, _ := api.OpMeta(op); !got.MCPImage {
		t.Fatalf("x-mm lacks mcp_image: %+v", got)
	}
	reg := a.OpenAPI().Components.Schemas
	body := reg.SchemaFromRef(op.Responses["200"].Content["application/json"].Schema.Ref)
	img := body.Properties["image"]
	if img == nil {
		t.Fatalf("body has no image property: %+v", body.Properties)
	}
	if img.Ref != "" {
		img = reg.SchemaFromRef(img.Ref)
	}
	if img.Properties["mime_type"] == nil || img.Properties["data_base64"] == nil {
		t.Fatalf("image schema = %+v", img.Properties)
	}
	req := map[string]bool{}
	for _, r := range img.Required {
		req[r] = true
	}
	if !req["mime_type"] || req["data_base64"] {
		t.Errorf("image.required = %v: mime_type must be required and data_base64 optional", img.Required)
	}
	for _, r := range body.Required {
		if r == "image" {
			t.Error("image must be optional in the body (inline=false omits it)")
		}
	}
}
