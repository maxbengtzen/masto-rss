#!/bin/sh
# Releases a version: scripts/release.sh patch | minor | major | X.Y.Z
#
# Checks that the working tree is clean and that CHANGELOG.md has something under Unreleased, runs
# the tests, writes the new version to VERSION, moves Unreleased to the version and creates the
# commit "chore(release): vX.Y.Z" with an annotated tag. Then push with `git push --follow-tags`.
set -eu
cd "$(dirname "$0")/.."

fail() {
	echo "release: $*" >&2
	exit 1
}

[ $# -eq 1 ] || fail "usage: scripts/release.sh patch | minor | major | X.Y.Z"
[ -z "$(git status --porcelain)" ] || fail "the working tree is not clean"

current=$(cat VERSION)
IFS=. read -r major minor patch <<EOV
$current
EOV
case "$1" in
patch) next="$major.$minor.$((patch + 1))" ;;
minor) next="$major.$((minor + 1)).0" ;;
major) next="$((major + 1)).0.0" ;;
*)
	echo "$1" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' || fail "unknown version: $1"
	next=$1
	;;
esac
! git rev-parse -q --verify "refs/tags/v$next" >/dev/null || fail "tag v$next already exists"

scripts/changelog.sh check
# The tests run with the new version in VERSION, since it appears in output the tests compare.
# Go doesn't need to be installed: without it the tests run in the Go image.
printf '%s\n' "$next" >VERSION
if command -v go >/dev/null; then
	test_cmd="go vet ./... && go test ./..."
	sh -c "$test_cmd" || { git checkout -q -- VERSION; fail "the tests failed"; }
else
	docker run --rm -u "$(id -u):$(id -g)" -e HOME=/tmp -v "$PWD":/src -w /src golang:1.27-alpine \
		sh -c 'go vet ./... && go test ./...' || { git checkout -q -- VERSION; fail "the tests failed"; }
fi

scripts/changelog.sh release "$next"
git add VERSION CHANGELOG.md
git commit -q -m "chore(release): v$next"
git tag -a "v$next" -m "chore(release): v$next"
echo "release: v$next created ($current → $next). Push with: git push --follow-tags"
