#!/usr/bin/env bash

[[ $DEBUG ]] && set -x

set -eof pipefail

PWD=`pwd`
BASEDIR="$(dirname ${BASH_SOURCE})"
KUBECTL="${BASEDIR}/kubectl"

echo "Starting etcd..."
docker run -d \
    --net=host \
    --name=etcd \
    quay.io/coreos/etcd:v$ETCD_VERSION

echo "Starting kubernetes..."

docker run -d --name=apiserver \
    --net=host --pid=host --privileged=true \
    gcr.io/google_containers/hyperkube:v${K8S_VERSION} \
    /hyperkube apiserver \
    --insecure-bind-address=0.0.0.0 \
    --service-cluster-ip-range=10.0.0.1/24 \
    --etcd_servers=http://127.0.0.1:4001 \
    --v=2

docker run -d --name=kubelet \
    --volume=/:/rootfs:ro \
    --volume=/sys:/sys:ro \
    --volume=/dev:/dev \
    --volume=/var/lib/docker/:/var/lib/docker:rw \
    --volume=/private/var/lib/kubelet/:/var/lib/kubelet:rw \
    --volume=/var/run:/var/run:rw \
    --net=host \
    --pid=host \
    --privileged=true \
    gcr.io/google_containers/hyperkube:v${K8S_VERSION} \
    /hyperkube kubelet \
    --containerized \
    --hostname-override="0.0.0.0" \
    --address="0.0.0.0" \
    --cluster_dns=10.0.0.10 --cluster_domain=cluster.local \
    --api-servers=http://localhost:8080 \
    --config=/etc/kubernetes/manifests-multi

docker run -d --name=proxy\
    --net=host \
    --privileged \
    gcr.io/google_containers/hyperkube:v${K8S_VERSION} \
    /hyperkube proxy \
    --master=http://0.0.0.0:8080

echo "Setting up kubectl..."

if [ ! -e ${KUBECTL} ]; then
  curl -sSL http://storage.googleapis.com/kubernetes-release/release/v${K8S_VERSION}/bin/linux/amd64/kubectl -o ${KUBECTL}
  chmod u+x ${KUBECTL}
fi

${KUBECTL} config set-cluster test-doc --server=http://0.0.0.0:8080
${KUBECTL} config set-context test-doc --cluster=test-doc
${KUBECTL} config use-context test-doc

# [w]ait [u]ntil [p]ort [i]s [a]ctually [o]pen
until curl -o /dev/null -sIf http://0.0.0.0:8080; do \
  sleep 5 && echo .;
done;

echo "Kubernetes started"
echo "Running nodes:"
${KUBECTL} get nodes
