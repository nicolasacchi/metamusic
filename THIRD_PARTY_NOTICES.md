# Third-party notices

## OpenSubsonic API description

`third_party/opensubsonic/openapi.json` is the OpenSubsonic API description (commit
`be131fb44a48fb346faa16f5a242785fdfe3052b`), licensed under the Apache License 2.0, copyright OpenSubsonic
contributors. See `third_party/opensubsonic/LICENSE` and `third_party/opensubsonic/NOTICE.md`. It is vendored as
test data only; the Subsonic bridge in `internal/subsonic` was written for this project from that description.

## SADIE II head-related impulse responses

`internal/dsp/hrtf/table.q15` is a derived table (256 taps, 16-bit, onset-aligned, diffuse-field equalised) built by
`tools/hrtfgen` from subject D1 (a Neumann KU100 dummy head) of the SADIE II database, copyright 2018 The University of York,
measured by Cal Armstrong, Lewis Thresh and Gavin Kearney at The Audio Lab, licensed under the Apache License 2.0
(doi:10.3390/app8112029). The full notice, the list of changes and the credit string are in `internal/dsp/hrtf/NOTICE`. The raw
dataset is not part of this repository.

## ACE-Step 1.5 and Qwen3 (worker image)

The ACE-Step worker image (`worker/Dockerfile.acestep`, built by CI, not part of the Go binary) runs ACE-Step 1.5 (code MIT,
copyright ACEStep; model weights MIT per the model cards) and redistributes models derived from Qwen3 by Alibaba Cloud
(Apache License 2.0; licence text in `worker/licenses/Qwen3-LICENSE-Apache-2.0.txt`). The complete notice, with the image's
Python dependency licences, is `worker/NOTICE.acestep`, which the image carries as `/opt/mm/THIRD_PARTY_NOTICES`.

## FluidR3_GM soundfont (app image)

The app image (`Dockerfile`, not part of the Go binary and not committed to this repository) carries `FluidR3_GM.sf2` from the Debian
package `fluid-soundfont-gm` 3.1-5.3, copyright 2000-2002, 2008 Frank Wen, released under the MIT license. It is fetched at build
time from the Debian snapshot archive by exact URL and sha256 and installed read-only at `/opt/mm/soundfonts/`. The licence text,
with the upstream copyright line verbatim, is `third_party/soundfonts/FluidR3_GM-LICENSE.txt`, which the image carries next to the file.

## fluidsynth (app image)

The app image installs the unmodified Alpine `fluidsynth` package (LGPL-2.1-or-later). It runs as a separate process started by the
score renderer; it is not linked into the metamusic binary. Its source is available from the Alpine package sources and from
https://www.fluidsynth.org/.

## abcjs (web bundle)

The web app bundles `abcjs` 6.7.3 (MIT, copyright 2009-2026 Paul Rosen and Gregory Dyke, no dependencies), pinned exactly in
`web/package.json` and `web/bun.lock`. It draws the notation of the Desk pages and is loaded on demand by those pages only; its
synthesizer is never used. The licence text ships in the package (`web/node_modules/abcjs/LICENSE.md`).
