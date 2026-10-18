#!/usr/bin/env bash
set -euo pipefail

guard="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/check-setup-go-pin.sh"
fixture=$(mktemp -d "${TMPDIR:-/tmp}/check-setup-go-pin.XXXXXX")
trap 'rm -rf "$fixture"' EXIT
mkdir -p "$fixture/.github/workflows"
cd "$fixture"

expect_pass() {
  if ! bash "$guard" >output.log 2>&1; then
    echo "FAIL: $1 (expected guard to pass)" >&2
    cat output.log >&2
    exit 1
  fi
  echo "PASS: $1"
}

expect_failure() {
  if bash "$guard" >output.log 2>&1; then
    echo "FAIL: $1 (expected guard to fail)" >&2
    cat output.log >&2
    exit 1
  fi
  echo "PASS: $1"
}

cat >.github/workflows/pinned.yml <<'YAML'
name: pinned
on: push
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/setup-go@90ad2b35f69faf97585ad74d28fa006d2739b7af
YAML
expect_pass 'pinned .yml only'

cat >.github/workflows/mutable.yaml <<'YAML'
name: mutable
on: push
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/setup-go@v7
YAML
expect_failure 'pinned .yml alongside mutable .yaml'

rm .github/workflows/mutable.yaml
cp .github/workflows/pinned.yml .github/workflows/pinned.yaml
expect_pass 'pinned .yml and .yaml'

rm .github/workflows/pinned.yml
expect_pass 'pinned .yaml only'

cat >.github/workflows/pinned.yaml <<'YAML'
name: no-setup-go
on: push
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - run: echo no setup-go references
YAML
expect_failure 'no setup-go references'

rm .github/workflows/pinned.yaml
expect_failure 'no workflow files'
