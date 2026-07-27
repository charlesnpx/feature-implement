#!/bin/sh

set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$repo_root"

fail() {
	printf '%s\n' "ci-baseline: $*" >&2
	exit 1
}

new_temp_dir() {
	temp_base=${TMPDIR:-/tmp}
	mktemp -d "$temp_base/feature-implement-ci.XXXXXX"
}

remove_temp_dir() {
	temp_dir=$1
	case "$temp_dir" in
		*/feature-implement-ci.*)
			rm -rf -- "$temp_dir"
			;;
		*)
			fail "refusing to remove unexpected temporary path: $temp_dir"
			;;
	esac
}

assert_head() {
	expected_head=${EXPECTED_HEAD_SHA:-}
	test -n "$expected_head" || fail "EXPECTED_HEAD_SHA is required"

	actual_head=$(git rev-parse HEAD)
	test "$actual_head" = "$expected_head" ||
		fail "HEAD $actual_head does not match requested head $expected_head"

	credential_config=$(git config --local --get-regexp '^(credential\.|http\..*\.extraheader$)' || true)
	test -z "$credential_config" ||
		fail "checkout contains persisted credential configuration"

	printf '%s\n' "verified_head=$actual_head"
}

run_normal() {
	go clean -testcache
	go test -count=1 ./...
}

run_shuffle() {
	shuffle_seed=${FEATURE_SHUFFLE_SEED:-$(date +%s)}
	printf '%s\n' "shuffle_seed=$shuffle_seed"
	go clean -testcache
	go test -count=1 -shuffle="$shuffle_seed" ./...
}

run_race() {
	go clean -testcache
	go test -count=1 -race -timeout=30m ./...
}

run_vet() {
	go vet ./...
}

run_build() {
	build_dir=$(new_temp_dir)
	trap 'remove_temp_dir "$build_dir"' EXIT HUP INT TERM
	go build -o "$build_dir/feature" ./cmd/feature
	"$build_dir/feature" version
	remove_temp_dir "$build_dir"
	trap - EXIT HUP INT TERM
}

run_installer() {
	stage_dir=$(new_temp_dir)
	trap 'remove_temp_dir "$stage_dir"' EXIT HUP INT TERM
	./install-skill.sh --plan --target all --json >/dev/null
	./install-skill.sh --install --target all --json --install-root "$stage_dir" >/dev/null
	"$stage_dir/.local/bin/feature" version
	remove_temp_dir "$stage_dir"
	trap - EXIT HUP INT TERM
}

run_diff() {
	format_files=$(git ls-files '*.go')
	test -n "$format_files" || fail "no tracked Go files found"
	format_diff=$(gofmt -d $format_files)
	test -z "$format_diff" || {
		printf '%s\n' "$format_diff" >&2
		fail "tracked Go files are not formatted"
	}

	go mod tidy -diff
	git diff --check
	git diff --exit-code
	git diff --cached --exit-code
}

run_clean() {
	tree_status=$(git status --porcelain=v1 --untracked-files=all)
	test -z "$tree_status" || {
		printf '%s\n' "$tree_status" >&2
		fail "repository is not clean"
	}
}

usage() {
	printf '%s\n' "usage: $0 assert-head|normal|shuffle|race|vet|build|installer|diff|clean|all" >&2
	exit 2
}

command_name=${1:-}
case "$command_name" in
	assert-head)
		assert_head
		;;
	normal)
		run_normal
		;;
	shuffle)
		run_shuffle
		;;
	race)
		run_race
		;;
	vet)
		run_vet
		;;
	build)
		run_build
		;;
	installer)
		run_installer
		;;
	diff)
		run_diff
		;;
	clean)
		run_clean
		;;
	all)
		assert_head
		run_normal
		run_shuffle
		run_race
		run_vet
		run_build
		run_installer
		run_diff
		run_clean
		;;
	*)
		usage
		;;
esac
