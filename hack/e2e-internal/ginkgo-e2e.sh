#!/usr/bin/env bash

[[ $DEBUG ]] && set -x

set -eof pipefail

# include env
. hack/e2e-internal/e2e-env.sh

echo "running ginkgo"

export PATH="${BASEDIR}/..":"${PATH}"
cd test/e2e
ginkgo -r \
    -keepGoing -- \
    -kubeconfig=$HOME/.kube/config \
    "${@:-}"
