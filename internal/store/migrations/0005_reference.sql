-- 0005_reference.sql — M2c: reference tracks (recipe-less) and lossy masters.

-- A lossy track's master is an MP3 file. One column; the shape is enforced by triggers (D-M2c-3).
ALTER TABLE tracks ADD COLUMN lossy INTEGER NOT NULL DEFAULT 0 CHECK (lossy IN (0,1));
CREATE INDEX tracks_lossy ON tracks(lossy) WHERE lossy = 1;
CREATE TRIGGER tracks_lossy_frozen BEFORE UPDATE OF lossy ON tracks
BEGIN SELECT RAISE(ABORT, 'tracks: frozen column'); END;

-- A stub recipe (engine 'unknown') has exactly this shape (D-M2c-1): {id, seed:0, title} and nothing else.
CREATE TRIGGER recipes_unknown_shape BEFORE INSERT ON recipes
WHEN NEW.engine = 'unknown' AND NOT (
       NEW.style = '' AND NEW.lyrics = '' AND NEW.cot = '' AND NEW.seed = 0
   AND NEW.sampling_json IS NULL AND NEW.created_by = 'import' AND NEW.core_hash = NEW.hash
   AND COALESCE(json_type(NEW.request_json, '$.title') = 'text', 0)
   AND (SELECT COUNT(*) FROM json_each(NEW.request_json)) = 3)
BEGIN SELECT RAISE(ABORT, 'recipes: unknown-engine shape'); END;

-- Lossy shape: the master is an .mp3 iff lossy; a lossy row is an untouched import (D-M2c-3, D-M2c-11).
CREATE TRIGGER tracks_lossy_shape BEFORE INSERT ON tracks
WHEN (NEW.lossy = 1) <> (lower(NEW.master_path) LIKE '%.mp3')
  OR (NEW.lossy = 1 AND (NEW.origin <> 'import' OR NEW.bits_per_sample <> 16 OR NEW.channels > 2
      OR NEW.source_sha256 <> NEW.master_sha256 OR NEW.frames_sha256 <> NEW.master_sha256
      OR NEW.audio_md5 <> '00000000000000000000000000000000'))
BEGIN SELECT RAISE(ABORT, 'tracks: lossy shape'); END;

-- import_rule 'reference' <=> the recipe is a stub; a lossy track is always a reference (D-M2c-4).
CREATE TRIGGER tracks_reference_rule BEFORE INSERT ON tracks
WHEN (NEW.import_rule = 'reference')
       <> COALESCE((SELECT engine = 'unknown' FROM recipes WHERE id = NEW.recipe_id), 0)
  OR (NEW.lossy = 1 AND NEW.import_rule <> 'reference')
BEGIN SELECT RAISE(ABORT, 'tracks: reference rule'); END;

-- A reference track never has a known-recipe parent or child: no mothers from references (D-M2c-6).
CREATE TRIGGER tracks_reference_lineage BEFORE INSERT ON tracks
WHEN NEW.parent_track_id IS NOT NULL
 AND (NEW.import_rule = 'reference')
       <> COALESCE((SELECT import_rule = 'reference' FROM tracks WHERE id = NEW.parent_track_id), 0)
BEGIN SELECT RAISE(ABORT, 'tracks: reference lineage'); END;

-- Lossy masters are served for both qualities; no MP3 rendition is ever stored for them.
CREATE TRIGGER renditions_lossy_no_mp3 BEFORE INSERT ON renditions
WHEN NEW.kind = 'mp3_320' AND COALESCE((SELECT lossy FROM tracks WHERE id = NEW.track_id), 0) = 1
BEGIN SELECT RAISE(ABORT, 'renditions: a lossy track has no mp3 rendition'); END;
CREATE TRIGGER renditions_lossy_no_mp3_upd BEFORE UPDATE OF kind, track_id ON renditions
WHEN NEW.kind = 'mp3_320' AND COALESCE((SELECT lossy FROM tracks WHERE id = NEW.track_id), 0) = 1
BEGIN SELECT RAISE(ABORT, 'renditions: a lossy track has no mp3 rendition'); END;

-- A draft can never have a reference track as its mother.
CREATE TRIGGER recipe_drafts_ref_parent_ins BEFORE INSERT ON recipe_drafts
WHEN NEW.parent_track_id IN (SELECT id FROM tracks WHERE import_rule = 'reference')
BEGIN SELECT RAISE(ABORT, 'recipe_drafts: recipe unknown'); END;
CREATE TRIGGER recipe_drafts_ref_parent_upd BEFORE UPDATE OF parent_track_id ON recipe_drafts
WHEN NEW.parent_track_id IN (SELECT id FROM tracks WHERE import_rule = 'reference')
BEGIN SELECT RAISE(ABORT, 'recipe_drafts: recipe unknown'); END;
