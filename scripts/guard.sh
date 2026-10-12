#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Forbidden-terms guard. Scans the index (tracked + staged files) and the
# commit messages of the current branch (commits not on main) for any term in
# a private list. The list is never committed.
#
# List sources, in order:
#   OHS_FORBIDDEN_TERMS  newline separated regexes (CI secret)
#   OHS_TERMS_FILE       path to a file, one regex per line
#
# Without a list the guard fails in GitHub Actions (GITHUB_ACTIONS set) and
# only warns elsewhere.
#
# The public repository, image and MCP registry names and the maintainer's
# public GitHub handle below are removed from every line
# before matching, so they never trip a term on their own. Only exact
# tokens are removed: an allowed name glued to more name characters
# (`evil-` before it, `-internal` after it) is left in place and still
# matched.
#
# Exit status: 0 clean, 1 a term matched, 2 the guard could not run (a term
# that is not a valid regex, a git grep error). It never passes on an error.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

allowed=(
  "ghcr.io/soma-intelligence/open-health-sync"
  "io.github.soma-intelligence/open-health-sync"
  "soma-intelligence/open-health-sync"
  "QuentinCar0n"
)

terms=""
if [ -n "${OHS_FORBIDDEN_TERMS:-}" ]; then
  terms="$OHS_FORBIDDEN_TERMS"
elif [ -n "${OHS_TERMS_FILE:-}" ]; then
  if [ -f "$OHS_TERMS_FILE" ]; then
    terms="$(cat "$OHS_TERMS_FILE")"
  else
    echo "guard: OHS_TERMS_FILE=$OHS_TERMS_FILE does not exist" >&2
  fi
fi

if [ -z "$(printf '%s' "$terms" | tr -d '[:space:]')" ]; then
  if [ -n "${GITHUB_ACTIONS:-}" ]; then
    echo "guard: ERROR no forbidden-terms list available (set OHS_FORBIDDEN_TERMS)" >&2
    exit 1
  fi
  echo "guard: WARNING no forbidden-terms list available (set OHS_TERMS_FILE), skipping" >&2
  exit 0
fi

# Removed in this order: keep a longer string before any string it contains.
OHS_GUARD_ALLOWED="$(printf '%s\n' "${allowed[@]}")"
export OHS_GUARD_ALLOWED

# match TERM: reads "path NUL line NUL text" records on stdin, removes the
# allowed strings from the text, prints the records where TERM still matches.
# Exit 0 when at least one record matched.
match() {
  perl -e '
    my $re = shift;
    my @allowed = grep { length } split /\n/, $ENV{OHS_GUARD_ALLOWED};
    my $hit = 0;
    local $/ = "\n";
    while (my $rec = <STDIN>) {
      my ($path, $line, $text) = split /\0/, $rec, 3;
      next unless defined $text;
      # A token boundary on both sides: no name character (word, dot,
      # hyphen) before it, no word character or hyphen after it. A slash
      # may precede it (a URL path) or follow it (a Go import path).
      for my $a (@allowed) { $text =~ s/(?<![\w.-])\Q$a\E(?![\w-])//g; }
      if ($text =~ /$re/i) { print "$path:$line:$text"; $hit = 1; }
    }
    exit($hit ? 0 : 1);
  ' "$1"
}

# Commit messages of the branch (only commits not on main, when main exists).
msgs=""
if git rev-parse --verify --quiet main >/dev/null; then
  msgs="$(git log main..HEAD --format=%B 2>/dev/null || true)"
fi

# PCRE (-P, perl for the second pass) because \b is not portable in git's
# default ERE on macOS. git grep finds candidate lines; match drops the ones
# that only matched inside an allowed string.
status=0
hits="$(mktemp "${TMPDIR:-/tmp}/ohs-guard.XXXXXX")"
trap 'rm -f "$hits"' EXIT
while IFS= read -r term; do
  case "$term" in ''|'#'*) continue ;; esac

  # A term perl cannot compile would make every check below fail quietly,
  # which reads as "no match": refuse it instead. The term itself is not
  # printed (the list is private).
  if ! perl -e 'qr/$ARGV[0]/i' "$term" 2>/dev/null; then
    echo "guard: ERROR a term of the list is not a valid regex" >&2
    exit 2
  fi

  # git grep runs on its own, not in a pipe, so its status is seen: 0 found,
  # 1 nothing found, anything else is an error.
  rc=0
  git grep --cached -z -nIiP -e "$term" -- . >"$hits" 2>/dev/null || rc=$?
  if [ "$rc" -gt 1 ]; then
    echo "guard: ERROR git grep failed (status $rc) on a term of the list" >&2
    exit 2
  fi
  if [ "$rc" -eq 0 ] && match "$term" <"$hits"; then
    echo "guard: forbidden term /$term/ found in files above" >&2
    status=1
  fi

  if [ -n "$msgs" ] && printf '%s\n' "$msgs" | perl -ne 'print "commit message\0$.\0$_"' | match "$term" >/dev/null; then
    echo "guard: forbidden term /$term/ found in a commit message" >&2
    status=1
  fi
done <<< "$terms"

if [ "$status" -eq 0 ]; then
  echo "guard: ok"
fi
exit "$status"
