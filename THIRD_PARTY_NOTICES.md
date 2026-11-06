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
