package api

import "github.com/danielgtaylor/huma/v2"

// ItemIn.Mode's enum tag lists the modes that exist today (the order the OpenAPI enum has always had). A track adds item_modes_<track>.go
// with an init() that calls addItemModes; TransformSchema appends the registered modes after the tag's, so the document, the request
// validation and the generated clients all carry one enum.

// TransformSchema appends the registered item modes to the schema of ItemIn.Mode.
func (ItemIn) TransformSchema(_ huma.Registry, s *huma.Schema) *huma.Schema {
	itemModesMu.Lock()
	defer itemModesMu.Unlock()
	if p := s.Properties["mode"]; p != nil {
		for _, m := range itemModes {
			p.Enum = append(p.Enum, m)
		}
	}
	return s
}
