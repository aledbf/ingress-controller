#!/usr/bin/env bash

[[ $DEBUG ]] && set -x

set -eof pipefail

# include env
. hack/e2e-internal/e2e-env.sh

echo "running ginkgo"

export PATH="${BASEDIR}/..":"${PATH}"
ginkgo -r \
    --ginkgo.flakeAttempts=2 \
    --repo-root="${BASEDIR}/.." \
    --prefix=e2e \
    --clean-start=true \
    "${@:-}"
