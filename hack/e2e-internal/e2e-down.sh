#!/usr/bin/env bash

[[ $DEBUG ]] && set -x

set -eof pipefail

# include env
. hack/e2e-internal/e2e-env.sh

# do not failt if the container is not running
docker rm -f hyperkube-installer || true
