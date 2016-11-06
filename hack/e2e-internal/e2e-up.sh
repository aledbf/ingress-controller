#!/usr/bin/env bash

[[ $DEBUG ]] && set -x

set -eof pipefail

# include env
. hack/e2e-internal/e2e-env.sh

mkdir -p /var/lib/kubelet
mount --bind /var/lib/kubelet /var/lib/kubelet
mount --make-shared /var/lib/kubelet

# do not failt if the container is not running
docker rm -f hyperkube-installer || true

echo "Starting kubernetes..."

docker run -d \
    --volume=/:/rootfs:ro \
    --volume=/sys:/sys:rw \
    --volume=/var/lib/docker/:/var/lib/docker:rw \
    --volume=/var/lib/kubelet/:/var/lib/kubelet:rw,shared \
    --volume=/var/run:/var/run:rw \
    --net=host \
    --pid=host \
    --name=hyperkube-installer \
    --privileged \
    gcr.io/google_containers/hyperkube:v${K8S_VERSION} \
    /hyperkube kubelet \
    --containerized \
    --hostname-override=127.0.0.1 \
    --api-servers=http://localhost:8080 \
    --config=/etc/kubernetes/manifests \
    --allow-privileged --v=2
  
echo "waiting until api server is available..."
until curl -o /dev/null -sIf http://0.0.0.0:8080; do \
  sleep 10;
done;

echo "Kubernetes started"

sleep 120

${KUBECTL} create -f test/images/clusterapi-tester.yaml
