# Open mHealth schemas and test data

`schemas/` and `official/` are copied, unchanged, from the official Open mHealth schema
repository, <https://github.com/openmhealth/schemas>, at commit
`df386000a93a35c1d7f33a023f759e41202a7d3d` (2026-08-26). They are released under the Apache
License 2.0, whose text is in `schemas/LICENSE`.

- `schemas/` holds the schemas the mapping emits and every schema they refer to. A `-N.x` file
  is a copy of the newest `N.m` version whose own references all resolve inside the repository
  (in the repository it is a symbolic link; some of the newest minor versions refer to IEEE
  1752.1 schemas, which are hosted elsewhere).
- `official/` is the repository's own test data (`shouldPass`, `shouldFail`) for those schemas;
  `TestOfficialExamples` runs it so a mistake in loading the schemas cannot go unnoticed.

`golden/` is ours: one data point per mapping, rewritten with `go test ./internal/omh -update`
and checked against `schemas/` by `TestGoldenFilesValidate`.

To refresh the copy, clone the repository and copy the same files from `schema/omh` and
`test-data/omh`, then run `go test ./internal/omh`.
