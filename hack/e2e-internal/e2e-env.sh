#!/bin/bash

export PWD=`pwd`
export BASEDIR="$(dirname ${BASH_SOURCE})"
export KUBECTL="${BASEDIR}/kubectl"
export K8S_VERSION="${K8S_VERSION:-1.4.4}"
export GOOS="${GOOS:-linux}"

echo "running: ${K8S_VERSION} ${KUBECTL}"

echo "checking if kubectl binary exists..."
if [ ! -e ${KUBECTL} ]; then
  echo "kubectl binary is missing. downloading..."
  curl -sSL http://storage.googleapis.com/kubernetes-release/release/v${K8S_VERSION}/bin/${GOOS}/amd64/kubectl -o ${KUBECTL}
  chmod u+x ${KUBECTL}
fi

${KUBECTL} config set-cluster travis --server=http://0.0.0.0:8080
${KUBECTL} config set-context travis --cluster=travis
${KUBECTL} config use-context travis
