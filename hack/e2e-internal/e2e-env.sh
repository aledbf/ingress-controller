#!/usr/bin/env bash

[[ $DEBUG ]] && set -x

export K8S_VERSION=1.4.5

export PWD=`pwd`
export BASEDIR="$(dirname ${BASH_SOURCE})"
export KUBECTL="/usr/local/bin/kubectl"
export GOOS="${GOOS:-linux}"

if [ ! -e ${KUBECTL} ]; then
  echo "kubectl binary is missing. downloading..."
  sudo curl -sSL http://storage.googleapis.com/kubernetes-release/release/v${K8S_VERSION}/bin/${GOOS}/amd64/kubectl -o ${KUBECTL}
  sudo chmod u+x ${KUBECTL}
fi

${KUBECTL} config set-cluster travis --server=http://0.0.0.0:8080
${KUBECTL} config set-context travis --cluster=travis
${KUBECTL} config use-context travis
