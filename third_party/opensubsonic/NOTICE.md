# OpenSubsonic API description

- Source: https://github.com/OpenSubsonic/open-subsonic-api (directory `openapi/`)
- Commit: `be131fb44a48fb346faa16f5a242785fdfe3052b` (2026-10-10)
- License: Apache License 2.0 (see `LICENSE`), copyright OpenSubsonic contributors
- Vendored on: 2026-10-15

`openapi.json` is the upstream `openapi/openapi.json` with every relative file reference
(`./schemas/*.json`, `./endpoints/*.json`, `./responses/*.json`) inlined into one document, so that the
description is self-contained: path items are expanded in place and every referenced file becomes an entry of
`components` named after its file (or the key the upstream root document gave it). Keys are sorted and the
file is indented by two spaces. No content was otherwise changed.

Used only as test data: the OpenSubsonic contract tests validate the responses of `internal/subsonic` against
these schemas. No code is generated from it, and nothing from GPL-licensed Subsonic servers or client
libraries is used.

sha256 of the vendored description: `61a861285f6bc3220f7a457c0f3161653cec769f1cd147118bed8d3557d8338c`
