#!/bin/sh
# Hanterar CHANGELOG.md (Keep a Changelog-format).
#
#   scripts/changelog.sh check            misslyckas om Unreleased är tomt
#   scripts/changelog.sh release <ver>    flyttar "## [Unreleased]" till "## [ver] - datum" och lägger
#                                         en ny tom Unreleased-rubrik överst
#   scripts/changelog.sh notes <ver>      skriver ut versionens avsnitt (release notes)
set -eu
file="$(dirname "$0")/../CHANGELOG.md"
unreleased='## [Unreleased]'

fail() {
	echo "changelog: $*" >&2
	exit 1
}

# Texten under en rubrik, fram till nästa "## [", utan tomma rader först och sist.
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
	body=$(section "$unreleased") || fail "rubriken \"$unreleased\" saknas"
	[ -n "$body" ] || fail "Unreleased är tomt: beskriv ändringarna innan du släpper en version"
}

case "${1:-}" in
check)
	check
	;;
release)
	version=${2:?ange version, t.ex. release 1.2.3}
	check
	! grep -qF "## [$version]" "$file" || fail "$version finns redan i CHANGELOG.md"
	date=$(date -u +%Y-%m-%d)
	awk -v heading="$unreleased" -v new="## [$version] - $date" '
		!done && $0 == heading { print; print ""; print new; done = 1; next }
		{ print }
	' "$file" >"$file.tmp" && mv "$file.tmp" "$file"
	echo "changelog: Unreleased → $version ($date)"
	;;
notes)
	version=${2:?ange version, t.ex. notes 1.2.3}
	version=${version#v}
	body=$(section "## [$version]") || fail "inget avsnitt för $version i CHANGELOG.md"
	[ -n "$body" ] || fail "inget avsnitt för $version i CHANGELOG.md"
	printf '%s\n' "$body"
	;;
*)
	fail "användning: changelog.sh check | release <version> | notes <version>"
	;;
esac
