#!/usr/bin/env bash
set -euo pipefail

expected='actions/setup-go@90ad2b35f69faf97585ad74d28fa006d2739b7af'

shopt -s nullglob
workflows=(.github/workflows/*.yml .github/workflows/*.yaml)
if [[ ${#workflows[@]} -eq 0 ]] || ! refs=$(grep -hEo "actions/setup-go@[^[:space:]\"'#,}]+" "${workflows[@]}"); then
  echo 'Could not find or read setup-go references in .github/workflows/*.{yml,yaml}' >&2
  exit 1
fi

refs=$(printf '%s\n' "$refs" | sort -u)
if [[ "$refs" != "$expected" ]]; then
  echo "Expected every setup-go reference to use $expected; mismatching refs:" >&2
  printf '%s\n' "$refs" | grep -Fvx "$expected" >&2
  exit 1
fi

echo 'All setup-go workflow references use the required immutable SHA.'
