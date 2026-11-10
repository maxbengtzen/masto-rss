#!/bin/sh
# Släpper en version: scripts/release.sh patch | minor | major | X.Y.Z
#
# Kontrollerar att arbetskatalogen är ren och att CHANGELOG.md har något under Unreleased, kör
# testerna, skriver den nya versionen i VERSION, flyttar Unreleased till versionen och skapar
# commiten "chore(release): vX.Y.Z" med en annoterad tagg. Pusha sedan med `git push --follow-tags`.
set -eu
cd "$(dirname "$0")/.."

fail() {
	echo "release: $*" >&2
	exit 1
}

[ $# -eq 1 ] || fail "användning: scripts/release.sh patch | minor | major | X.Y.Z"
[ -z "$(git status --porcelain)" ] || fail "arbetskatalogen är inte ren"

current=$(cat VERSION)
IFS=. read -r major minor patch <<EOV
$current
EOV
case "$1" in
patch) next="$major.$minor.$((patch + 1))" ;;
minor) next="$major.$((minor + 1)).0" ;;
major) next="$((major + 1)).0.0" ;;
*)
	echo "$1" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' || fail "okänd version: $1"
	next=$1
	;;
esac
! git rev-parse -q --verify "refs/tags/v$next" >/dev/null || fail "taggen v$next finns redan"

scripts/changelog.sh check
# Testerna körs med den nya versionen i VERSION, eftersom den syns i utdata som testerna jämför.
# Go behöver inte finnas på datorn: utan den körs testerna i Go-imagen.
printf '%s\n' "$next" >VERSION
if command -v go >/dev/null; then
	test_cmd="go vet ./... && go test ./..."
	sh -c "$test_cmd" || { git checkout -q -- VERSION; fail "testerna gick inte igenom"; }
else
	docker run --rm -u "$(id -u):$(id -g)" -e HOME=/tmp -v "$PWD":/src -w /src golang:1.27-alpine \
		sh -c 'go vet ./... && go test ./...' || { git checkout -q -- VERSION; fail "testerna gick inte igenom"; }
fi

scripts/changelog.sh release "$next"
git add VERSION CHANGELOG.md
git commit -q -m "chore(release): v$next"
git tag -a "v$next" -m "chore(release): v$next"
echo "release: v$next skapad ($current → $next). Pusha med: git push --follow-tags"
