#!/bin/sh
# Manages CHANGELOG.md (Keep a Changelog format).
#
#   scripts/changelog.sh check            fails if Unreleased is empty
#   scripts/changelog.sh release <ver>    moves "## [Unreleased]" to "## [ver] - date" and adds
#                                         a new empty Unreleased heading at the top
#   scripts/changelog.sh notes <ver>      prints the version's section (release notes)
set -eu
file="$(dirname "$0")/../CHANGELOG.md"
unreleased='## [Unreleased]'

fail() {
	echo "changelog: $*" >&2
	exit 1
}

# The text under a heading, up to the next "## [", without leading and trailing blank lines.
section() {
	awk -v heading="$1" '
		index($0, heading) == 1 { found = 1; next }
		found && /^## \[/ { exit }
		found { lines[++n] = $0 }
		END {
			if (!found) exit 2
			first = 1; while (first <= n && lines[first] ~ /^[[:space:]]*$/) first++
			last = n; while (last >= first && lines[last] ~ /^[[:space:]]*$/) last--
			for (i = first; i <= last; i++) print lines[i]
		}
	' "$file"
}

check() {
	body=$(section "$unreleased") || fail "heading \"$unreleased\" is missing"
	[ -n "$body" ] || fail "Unreleased is empty: describe the changes before releasing a version"
}

case "${1:-}" in
check)
	check
	;;
release)
	version=${2:?give a version, e.g. release 1.2.3}
	check
	! grep -qF "## [$version]" "$file" || fail "$version already exists in CHANGELOG.md"
	date=$(date -u +%Y-%m-%d)
	awk -v heading="$unreleased" -v new="## [$version] - $date" '
		!done && $0 == heading { print; print ""; print new; done = 1; next }
		{ print }
	' "$file" >"$file.tmp" && mv "$file.tmp" "$file"
	echo "changelog: Unreleased → $version ($date)"
	;;
notes)
	version=${2:?give a version, e.g. notes 1.2.3}
	version=${version#v}
	body=$(section "## [$version]") || fail "no section for $version in CHANGELOG.md"
	[ -n "$body" ] || fail "no section for $version in CHANGELOG.md"
	printf '%s\n' "$body"
	;;
*)
	fail "usage: changelog.sh check | release <version> | notes <version>"
	;;
esac
