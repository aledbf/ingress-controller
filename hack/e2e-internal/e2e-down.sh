#!/usr/bin/env bash

[[ $DEBUG ]] && set -x

set -eof pipefail

# include env
. ./e2e-env.sh

echo "Destroying running docker containers..."
docker rm -f kubelet
docker rm -f apiserver
docker rm -f etcd
