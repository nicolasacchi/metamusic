package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"github.com/nicolasacchi/metamusic/internal/auth"
	"github.com/nicolasacchi/metamusic/internal/engine/registry"
	"github.com/nicolasacchi/metamusic/internal/engineadopt"
	"github.com/nicolasacchi/metamusic/internal/jobs"
	"github.com/nicolasacchi/metamusic/internal/store"
	"github.com/nicolasacchi/metamusic/internal/trackctx"
)

// buildE2Ops wires the collaborators of the engine operations (C160): lane E2-OP owns this file. It hands the provider admin the
// spend service its global worker bound is compared with (CONCURRENCY_EXCEEDS_CAP, C151.5) and sets the trackctx EngineAdvisor
// (provenance.engine and the engine-aware next actions, C149.4). list_engines and get_provider_status read their collaborators
// from the Services fields buildE2 already fills.
func buildE2Ops(in e2Inputs, e *e2Services) {
	if in.m3.Provider != nil {
		in.m3.Provider.UseBound(jobs.BoundInputs{Spend: in.m3.Spend})
	}
	if in.ctxb != nil {
		in.ctxb.UseEngines(&engineAdvisor{st: in.st, engines: e.Engines, adoption: e.Adoption})
	}
}

// engineAdvisor answers /context for the takes of an engine other than yue2. A yue2 take with a yue2 parent (every take that
// exists before E2) gets the zero advice, so its bundle is byte-identical to the yue2-only behaviour.
type engineAdvisor struct {
	st       *store.Store
	engines  registry.Registry
	adoption *engineadopt.Service
}

var _ trackctx.EngineAdvisor = (*engineAdvisor)(nil)

const yue2Engine = "yue2"

func (a *engineAdvisor) Advise(ctx context.Context, p auth.Principal, trackID string, _ int) (trackctx.EngineAdvice, error) {
	var eng, request, parent string
	err := a.st.R.QueryRowContext(ctx, `SELECT r.engine, r.request_json, COALESCE(pr.engine, '')
		FROM tracks t JOIN recipes r ON r.id = t.recipe_id
		LEFT JOIN tracks pt ON pt.id = t.parent_track_id LEFT JOIN recipes pr ON pr.id = pt.recipe_id
		WHERE t.id = ?`, trackID).Scan(&eng, &request, &parent)
	if errors.Is(err, sql.ErrNoRows) {
		return trackctx.EngineAdvice{}, nil
	}
	if err != nil {
		return trackctx.EngineAdvice{}, err
	}
	cross := parent != "" && parent != eng
	if eng == yue2Engine {
		if !cross {
			return trackctx.EngineAdvice{}, nil
		}
		return trackctx.EngineAdvice{Engine: eng, CrossEngine: true}, nil
	}
	adv := trackctx.EngineAdvice{Engine: eng, CrossEngine: cross, BlockYue2Actions: true}
	var req struct {
		Task string `json:"task"`
	}
	_ = json.Unmarshal([]byte(request), &req) // a request this engine cannot parse still gets its engine key
	adv.Task = req.Task
	st, err := a.adoption.State(ctx, a.st.R, eng)
	if err != nil {
		return adv, err
	}
	in := trackctx.ACERuleInput{Adopted: st.Adopted(), CanGenerate: p.Has(auth.ScopeGenerate), TaskIsRender: true}
	if en, ok := a.engines[eng]; ok {
		c := en.Caps()
		has := func(t string) bool { return slices.Contains(c.Tasks, t) }
		in.RepaintOK, in.ExtendOK = c.RepaintInterval && has("repaint"), c.Extend && has("extend")
		in.LyricEditOK, in.ReferenceOK = has("lyric_edit"), c.ReferenceAudio && has("reference")
	}
	adv.Actions = trackctx.ACENextActions(in)
	return adv, nil
}
