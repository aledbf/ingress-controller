#!/usr/bin/env bash

[[ $DEBUG ]] && set -x

set -eof pipefail

# include env
. ./e2e-env.sh

echo "Kubernetes information:"
${KUBECTL} version
