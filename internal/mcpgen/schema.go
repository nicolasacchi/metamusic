package mcpgen

import (
	"fmt"
	"slices"
	"strings"
)

const refPrefix = "#/components/schemas/"

// param maps one tool argument to a wire parameter.
type param struct {
	Arg      string // tool argument name
	Wire     string // name on the wire (path/query/header)
	In       string // path | query | header
	Required bool
}

// bodyMode says how the request body is represented in tool arguments.
type bodyMode int

const (
	bodyNone   bodyMode = iota
	bodyFlat            // body properties are merged into the tool arguments
	bodyNested          // the whole body lives under the "body" argument
)

// toolSpec is everything needed to list and dispatch one operation.
type toolSpec struct {
	ID, Method, Path string
	Description      string
	Params           []param
	Body             bodyMode
	BodyProps        []string // flat mode: property names owned by the body
	Input            map[string]any
	Output           map[string]any // nil when there is no object-shaped 2xx JSON response
}

// snake converts a header-style name (Idempotency-Key) to snake_case.
func snake(s string) string {
	return strings.ToLower(strings.NewReplacer("-", "_", " ", "_").Replace(s))
}

// inliner resolves component $refs. Non-recursive refs are inlined; recursive
// ones are emitted once under $defs.
type inliner struct {
	comps map[string]any
	defs  map[string]any
	err   error // first unresolvable $ref
}

func newInliner(comps map[string]any) *inliner {
	return &inliner{comps: comps, defs: map[string]any{}}
}

func (in *inliner) resolve(v any, stack []string) any {
	switch t := v.(type) {
	case map[string]any:
		if ref, ok := t["$ref"].(string); ok && strings.HasPrefix(ref, refPrefix) {
			name := strings.TrimPrefix(ref, refPrefix)
			target, ok := in.comps[name]
			if !ok {
				if in.err == nil {
					in.err = fmt.Errorf("unresolvable $ref %q", ref)
				}
				return t
			}
			if slices.Contains(stack, name) {
				if _, done := in.defs[name]; !done {
					in.defs[name] = nil // placeholder stops re-entry
					in.defs[name] = in.resolve(target, []string{name})
				}
				return map[string]any{"$ref": "#/$defs/" + name}
			}
			return in.resolve(target, append(slices.Clone(stack), name))
		}
		out := make(map[string]any, len(t))
		for k, x := range t {
			if k == "$schema" {
				continue
			}
			out[k] = in.resolve(x, stack)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = in.resolve(x, stack)
		}
		return out
	}
	return v
}

func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }

func jsonSchemaOf(content any) any {
	c := asMap(content)
	for _, ct := range []string{"application/json"} {
		if e := asMap(c[ct]); e != nil {
			if s, ok := e["schema"]; ok {
				return s
			}
		}
	}
	return nil
}

// buildSpec derives a toolSpec from one OpenAPI operation object.
func buildSpec(id, method, path string, op map[string]any, comps map[string]any) (*toolSpec, error) {
	in := newInliner(comps)  // parameters + request body
	out := newInliner(comps) // response
	spec := &toolSpec{ID: id, Method: method, Path: path}
	summary, _ := op["summary"].(string)
	desc, _ := op["description"].(string)
	spec.Description = summary + "\n\n" + desc

	props := map[string]any{}
	var required []string
	owner := map[string]string{}
	add := func(name string, schema any, req bool, who string) error {
		if prev, dup := owner[name]; dup {
			return fmt.Errorf("%s: argument %q declared by both %s and %s", id, name, prev, who)
		}
		owner[name] = who
		props[name] = schema
		if req {
			required = append(required, name)
		}
		return nil
	}

	// Parameters first: they win names over body properties.
	plist, _ := op["parameters"].([]any)
	for _, raw := range plist {
		p := asMap(raw)
		name, _ := p["name"].(string)
		loc, _ := p["in"].(string)
		if name == "" || (loc != "path" && loc != "query" && loc != "header") {
			continue // cookies are never exposed
		}
		arg := name
		if loc == "header" {
			arg = snake(name)
		}
		req, _ := p["required"].(bool)
		if loc == "path" {
			req = true
		}
		schema := asMap(in.resolve(p["schema"], nil))
		if schema == nil {
			schema = map[string]any{}
		}
		if d, ok := p["description"].(string); ok && schema["description"] == nil {
			schema["description"] = d
		}
		if err := add(arg, schema, req, loc+" parameter"); err != nil {
			return nil, err
		}
		spec.Params = append(spec.Params, param{Arg: arg, Wire: name, In: loc, Required: req})
	}

	// Request body.
	if rb := asMap(op["requestBody"]); rb != nil {
		if raw := jsonSchemaOf(rb["content"]); raw != nil {
			bodyReq, _ := rb["required"].(bool)
			bs := asMap(in.resolve(raw, nil))
			bprops := asMap(bs["properties"])
			collides := false
			for n := range bprops {
				if _, dup := owner[n]; dup {
					collides = true
				}
			}
			flat := bs["type"] == "object" && len(bprops) > 0 && !collides
			if flat {
				spec.Body = bodyFlat
				for n, s := range bprops {
					spec.BodyProps = append(spec.BodyProps, n)
					props[n] = s
					owner[n] = "body"
				}
				slices.Sort(spec.BodyProps)
				if r, ok := bs["required"].([]any); ok {
					for _, x := range r {
						if n, ok := x.(string); ok {
							required = append(required, n)
						}
					}
				}
			} else {
				spec.Body = bodyNested
				if err := add("body", bs, bodyReq, "request body"); err != nil {
					return nil, err
				}
			}
		}
	}

	slices.Sort(required)
	required = slices.Compact(required)
	input := map[string]any{
		"type":                 "object",
		"properties":           props,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		input["required"] = required
	}

	// Output: first 2xx JSON response.
	var output map[string]any
	if resp := asMap(op["responses"]); resp != nil {
		codes := make([]string, 0, len(resp))
		for c := range resp {
			if strings.HasPrefix(c, "2") {
				codes = append(codes, c)
			}
		}
		slices.Sort(codes)
		for _, c := range codes {
			if raw := jsonSchemaOf(asMap(resp[c])["content"]); raw != nil {
				if s := asMap(out.resolve(raw, nil)); s != nil && s["type"] == "object" {
					output = s
				}
				break
			}
		}
	}
	if len(in.defs) > 0 {
		input["$defs"] = in.defs
	}
	if output != nil && len(out.defs) > 0 {
		output["$defs"] = out.defs
	}
	if in.err != nil {
		return nil, fmt.Errorf("%s: %w", id, in.err)
	}
	if out.err != nil {
		return nil, fmt.Errorf("%s: %w", id, out.err)
	}
	spec.Input, spec.Output = input, output
	return spec, nil
}
