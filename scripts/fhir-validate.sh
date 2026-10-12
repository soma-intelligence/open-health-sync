#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Validates the FHIR golden files (internal/fhir/testdata) with the official
# HL7 FHIR validator against FHIR R4 (4.0.1). Not part of the required CI: it
# needs Java 17 or later and the network (the validator jar from its GitHub
# release, the FHIR packages from packages.fhir.org, and by default the
# terminology server tx.fhir.org, which also checks the LOINC and UCUM codes).
#
#   scripts/fhir-validate.sh            # or: make fhir-validate
#
# FHIR_VALIDATOR_DIR   where the jar is downloaded and the validator runs
#                      (default: a new temporary directory, kept out of the tree)
# FHIR_VALIDATOR_TX    terminology server; "n/a" validates offline, without
#                      code checks (default: the validator's own default)
#
# Exits non-zero when any file has an error; warnings are printed, not fatal.
set -euo pipefail

version="7.0.1"
sha256="1178b6a3a3fe98d9cd9545cadee505274967332466001d857e4d8bed7138a5bc"
url="https://github.com/hapifhir/org.hl7.fhir.core/releases/download/$version/validator_cli.jar"

root="$(cd "$(dirname "$0")/.." && pwd)"
dir="${FHIR_VALIDATOR_DIR:-$(mktemp -d)}"
mkdir -p "$dir"
jar="$dir/validator_cli-$version.jar"

command -v java >/dev/null || { echo "fhir-validate: java (17 or later) is required" >&2; exit 2; }
if [ ! -s "$jar" ]; then
  echo "fhir-validate: downloading the HL7 validator $version to $dir"
  curl -fsSL -o "$jar.part" "$url"
  mv "$jar.part" "$jar"
fi
got="$(sha256sum "$jar" | cut -d' ' -f1)"
if [ "$got" != "$sha256" ]; then
  echo "fhir-validate: $jar has sha256 $got, want $sha256" >&2
  exit 1
fi

files=("$root"/internal/fhir/testdata/*.json)
args=("${files[@]}" -version 4.0.1)
if [ -n "${FHIR_VALIDATOR_TX:-}" ]; then
  args+=(-tx "$FHIR_VALIDATOR_TX")
fi
# Run from the download directory: the validator writes its caches there.
cd "$dir"
java -Xmx4g -jar "$jar" "${args[@]}"
