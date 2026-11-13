#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Validates the FHIR golden files (internal/fhir/testdata) with the official
# HL7 FHIR validator against FHIR R4 (4.0.1), then the Physical Activity golden
# files a second time against the HL7 Physical Activity IG's activity measure
# profile (hl7.fhir.us.physical-activity 1.0.0). CI runs it on the changes that
# touch internal/fhir (.github/workflows/fhir-validate.yml). It
# needs Java 17 or later and the network (the validator jar from its GitHub
# release, the FHIR packages from packages.fhir.org, and by default the
# terminology server tx.fhir.org, which also checks the LOINC and UCUM codes).
#
#   scripts/fhir-validate.sh            # or: make fhir-validate
#   scripts/fhir-validate.sh DIR        # the *.json in DIR instead
#
# With a directory, it validates the files the server itself served
# (test/synthflow TestEveryOutputConforms writes them with OHS_FHIR_OUT=DIR):
# every file against R4, then the Observations that carry the Physical
# Activity IG category against its activity measure profile.
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

tx=()
if [ -n "${FHIR_VALIDATOR_TX:-}" ]; then
  tx=(-tx "$FHIR_VALIDATOR_TX")
fi
if [ $# -gt 0 ]; then
  src="$(cd "$1" && pwd)"
  files=("$src"/*.json)
  [ -e "${files[0]}" ] || { echo "fhir-validate: no .json file in $1" >&2; exit 2; }
  # The Observations the server marks as Physical Activity IG measures.
  mapfile -t pa_files < <(grep -l '"code": "PhysicalActivity"' "$src"/observation-*.json || true)
else
  files=("$root"/internal/fhir/testdata/*.json)
  # The Observations that are Physical Activity IG measures (docs/fhir.md).
  pa_files=(
    "$root"/internal/fhir/testdata/observation-activity-moderate-time.json
    "$root"/internal/fhir/testdata/observation-activity-vigorous-time.json
    "$root"/internal/fhir/testdata/observation-workout.json
  )
fi
pa_ig="hl7.fhir.us.physical-activity#1.0.0"
pa_profile="http://hl7.org/fhir/us/physical-activity/StructureDefinition/pa-observation-activity-measure"

# Run from the download directory: the validator writes its caches there.
cd "$dir"
java -Xmx4g -jar "$jar" "${files[@]}" -version 4.0.1 "${tx[@]}"
if [ ${#pa_files[@]} -eq 0 ]; then
  echo "fhir-validate: no Physical Activity IG measure to check"
  exit 0
fi
echo "fhir-validate: Physical Activity IG ($pa_ig), profile $pa_profile, ${#pa_files[@]} files"
java -Xmx4g -jar "$jar" "${pa_files[@]}" -version 4.0.1 -ig "$pa_ig" -profile "$pa_profile" "${tx[@]}"
