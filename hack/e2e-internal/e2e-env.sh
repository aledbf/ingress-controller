#!/bin/bash

export PWD=`pwd`
export BASEDIR="$(dirname ${BASH_SOURCE})"
export KUBECTL="${BASEDIR}/kubectl"

${KUBECTL} config set-cluster travis --server=http://0.0.0.0:8080
${KUBECTL} config set-context travis --cluster=travis
${KUBECTL} config use-context travis

echo "checking if kubectl binary exists..."
if [ ! -e ${KUBECTL} ]; then
  curl -sSL http://storage.googleapis.com/kubernetes-release/release/v${K8S_VERSION}/bin/linux/amd64/kubectl -o ${KUBECTL}
  chmod u+x ${KUBECTL}
fi
