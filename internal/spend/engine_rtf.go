package spend

import (
	"bytes"
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/nicolasacchi/metamusic/internal/store"
)

// Per-engine real-time factors (C152, D-E2-17, D-E2-32). yue2 keeps rtf_stats, RTFMilli and RecordRTF untouched; every
// other engine reads engine_rtf rows (measurements, written only by job:, canary: or human: writers) and falls back to
// the committed seed file (unsourced placeholders until the canary measures).

//go:embed engine_rtf.seed.json
var engineRTFSeedJSON []byte

type seedCell struct {
	RTFMilli int `json:"rtf_milli"`
	LoadS    int `json:"load_s"`
}

type seedFile struct {
	Version int                                       `json:"version"`
	Sourced bool                                      `json:"sourced"`
	Engines map[string]map[string]map[string]seedCell `json:"engines"`
}

var loadSeeds = sync.OnceValues(func() (seedFile, error) { return parseSeeds(engineRTFSeedJSON) })

func parseSeeds(raw []byte) (seedFile, error) {
	var f seedFile
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return seedFile{}, fmt.Errorf("spend: engine_rtf seed: %w", err)
	}
	if f.Version != 1 {
		return seedFile{}, fmt.Errorf("spend: engine_rtf seed: unsupported version %d", f.Version)
	}
	for eng, byClass := range f.Engines {
		for class, byGPU := range byClass {
			for gpu, c := range byGPU {
				if gpu != GPUClassKey(gpu) || c.RTFMilli < RTFMin || c.RTFMilli > RTFMax || c.LoadS < 0 || c.LoadS > 600 {
					return seedFile{}, fmt.Errorf("spend: engine_rtf seed: bad cell %s/%s/%s", eng, class, gpu)
				}
			}
		}
	}
	return f, nil
}

type measuredRow struct {
	rawClass            string
	rtf, loadS, samples int
}

// readMeasured returns the engine_rtf rows of (engine, class) keyed by canonical GPU class.
func readMeasured(ctx context.Context, q Querier, engine, class string) (map[string]measuredRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT gpu_class, rtf_milli, fixed_s, samples FROM engine_rtf WHERE engine = ? AND task_class = ? ORDER BY gpu_class`, engine, class)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]measuredRow{}
	for rows.Next() {
		var r measuredRow
		if err := rows.Scan(&r.rawClass, &r.rtf, &r.loadS, &r.samples); err != nil {
			return nil, err
		}
		k := GPUClassKey(r.rawClass)
		if prev, dup := out[k]; !dup || r.rawClass == k || prev.rawClass != k {
			out[k] = r
		}
	}
	return out, rows.Err()
}

// EngineRTF is the maximum rtf_milli and load_s over the allowed GPU classes: one class's value is its engine_rtf row,
// else the committed seed, else ok is false (the caller answers 503 GENERATION_DISABLED reason no_rtf). A class without
// either makes the whole answer not ok: an unknown card never lowers the estimate. With no allowed classes the maximum
// over every known class of the engine and task class is used.
func (s *Service) EngineRTF(ctx context.Context, q Querier, engine, class string, gpuClasses []string) (rtfMilli, loadS int, ok bool, err error) {
	seeds, err := loadSeeds()
	if err != nil {
		return 0, 0, false, err
	}
	measured, err := readMeasured(ctx, q, engine, class)
	if err != nil {
		return 0, 0, false, fmt.Errorf("spend: engine rtf: %w", err)
	}
	seed := seeds.Engines[engine][class]
	cell := func(k string) (seedCell, bool) {
		if m, hit := measured[k]; hit {
			return seedCell{m.rtf, m.loadS}, true
		}
		c, hit := seed[k]
		return c, hit
	}
	var keys []string
	for _, g := range gpuClasses {
		if k := GPUClassKey(g); k != "" {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		for k := range seed {
			keys = append(keys, k)
		}
		for k := range measured {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return 0, 0, false, nil
	}
	for _, k := range keys {
		c, hit := cell(k)
		if !hit {
			return 0, 0, false, nil
		}
		rtfMilli, loadS = max(rtfMilli, c.RTFMilli), max(loadS, c.LoadS)
	}
	return rtfMilli, loadS, true, nil
}

// EngineRTFRows lists the rows list_engines and get_provider_status show for an engine: measured rows, and seeds for the
// classes that have no measured row, ordered by task class then GPU class.
func (s *Service) EngineRTFRows(ctx context.Context, q Querier, engine string) ([]EngineRTFRow, error) {
	seeds, err := loadSeeds()
	if err != nil {
		return nil, err
	}
	var out []EngineRTFRow
	seen := map[[2]string]bool{}
	rows, err := q.QueryContext(ctx, `SELECT task_class, gpu_class, rtf_milli, fixed_s, samples FROM engine_rtf WHERE engine = ? ORDER BY task_class, gpu_class`, engine)
	if err != nil {
		return nil, fmt.Errorf("spend: engine rtf rows: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		r := EngineRTFRow{Engine: engine, Source: "measured"}
		if err := rows.Scan(&r.TaskClass, &r.GPUClass, &r.RTFMilli, &r.LoadS, &r.Samples); err != nil {
			return nil, err
		}
		seen[[2]string{r.TaskClass, GPUClassKey(r.GPUClass)}] = true
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for class, byGPU := range seeds.Engines[engine] {
		for gpu, c := range byGPU {
			if !seen[[2]string{class, gpu}] {
				out = append(out, EngineRTFRow{Engine: engine, TaskClass: class, GPUClass: gpu, RTFMilli: c.RTFMilli, LoadS: c.LoadS, Source: "seed"})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TaskClass != out[j].TaskClass {
			return out[i].TaskClass < out[j].TaskClass
		}
		return out[i].GPUClass < out[j].GPUClass
	})
	return out, nil
}

// RecordEngineRTF is the EWMA step of NextRTF for (engine, task class, GPU class). e2eMS is the take's generation time
// already net of its fixed load (generate_s + save_s - load_s), audioMS the rendered audio; the caller applies the
// learning rule of C152 (full-length render of an enabled task of the class). by is job:<id>, canary:<id> or human:<who>
// (the table CHECK enforces the same). The first sample of a class stores the observation itself, with the class's seeded
// load (else LoadS). It runs in the caller's transaction. yue2 is refused: it learns in rtf_stats.
func (s *Service) RecordEngineRTF(ctx context.Context, tx *sql.Tx, engine, class, gpuClass, by string, e2eMS, audioMS int64) error {
	if engine == "" || engine == "yue2" {
		return errors.New("spend: engine rtf: engine must be a non-yue2 engine")
	}
	if class != "turbo" && class != "base" {
		return fmt.Errorf("spend: engine rtf: unknown task class %q", class)
	}
	key := GPUClassKey(gpuClass)
	if key == "" {
		return errors.New("spend: engine rtf: empty gpu class")
	}
	var canary any
	switch {
	case strings.HasPrefix(by, "canary:") && len(by) > len("canary:"):
		canary = strings.TrimPrefix(by, "canary:")
	case strings.HasPrefix(by, "job:") && len(by) > len("job:"), strings.HasPrefix(by, "human:") && len(by) > len("human:"):
	default:
		return fmt.Errorf("spend: engine rtf: writer %q is not a job, canary or human writer", by)
	}
	if audioMS <= 0 {
		return errors.New("spend: engine rtf: no audio")
	}
	obs := ObservedRTF(e2eMS, audioMS)
	measured, err := readMeasured(ctx, tx, engine, class)
	if err != nil {
		return fmt.Errorf("spend: record engine rtf: %w", err)
	}
	now := store.FormatTime(s.now())
	if hit, ok := measured[key]; ok {
		_, err = tx.ExecContext(ctx, `UPDATE engine_rtf SET rtf_milli = ?, samples = samples + 1, updated_at = ?, updated_by = ?, canary_id = ?
			WHERE engine = ? AND task_class = ? AND gpu_class = ?`, NextRTF(hit.rtf, obs), now, by, canary, engine, class, hit.rawClass)
	} else {
		load := LoadS
		if seeds, serr := loadSeeds(); serr == nil {
			if c, ok := seeds.Engines[engine][class][key]; ok {
				load = c.LoadS
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO engine_rtf(engine, task_class, gpu_class, rtf_milli, fixed_s, samples, updated_at, updated_by, canary_id)
			VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?)`, engine, class, key, obs, load, now, by, canary)
	}
	if err != nil {
		return fmt.Errorf("spend: record engine rtf: %w", err)
	}
	return nil
}
