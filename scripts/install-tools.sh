#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
export GOBIN="$PWD/artifacts/tools"
mkdir -p "$GOBIN"
install_go() {
  local package="$1" version="$2" binary="$3"
  if [[ -x "$GOBIN/$binary" ]] && go version -m "$GOBIN/$binary" | awk -v version="$version" '$1 == "mod" && $3 == version { found=1 } END { exit !found }'; then return; fi
  go install "$package@$version"
}
install_go mvdan.cc/gofumpt v0.12.0 gofumpt
install_go golang.org/x/tools/cmd/goimports v0.50.0 goimports
install_go honnef.co/go/tools/cmd/staticcheck v0.8.1 staticcheck
install_go github.com/securego/gosec/v2/cmd/gosec v2.29.0 gosec
install_go golang.org/x/vuln/cmd/govulncheck v1.8.0 govulncheck
install_go github.com/rhysd/actionlint/cmd/actionlint v1.7.12 actionlint
python3 scripts/install-lint-binaries.py
