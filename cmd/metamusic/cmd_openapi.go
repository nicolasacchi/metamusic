package main

import (
	"net/http"

	"github.com/nicolasacchi/metamusic/internal/api"
)

func cmdOpenAPI(s streams) error {
	a := api.New(http.NewServeMux(), api.Deps{Version: version, Commit: commit})
	b, err := api.OpenAPIJSON(a)
	if err != nil {
		return err
	}
	_, err = s.out.Write(b)
	return err
}
