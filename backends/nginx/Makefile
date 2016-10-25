all: push

BUILDTAGS=

# 0.0 shouldn't clobber any release builds
RELEASE = 0.9
PREFIX = quay.io/aledbf/nginx-ingress-controller
GOOS?=linux

REPO_INFO=$(shell git config --get remote.origin.url)

ifndef COMMIT
  COMMIT := git-$(shell git rev-parse --short HEAD)
endif

PKG := "github.com/aledbf/ingress-controller/backends/nginx"

build: clean
	CGO_ENABLED=0 GOOS=${GOOS} go build -a -installsuffix cgo \
		-ldflags "-s -w -X ${PKG}/pkg/version.RELEASE=${RELEASE} -X ${PKG}/pkg/version.COMMIT=${COMMIT} -X ${PKG}/pkg/version.REPO=${REPO_INFO}" \
		-o rootfs/ingress-controller/nginx ${PKG}/pkg/cmd/controller

container: build
	docker build -t $(PREFIX):$(RELEASE) rootfs

push: container
	gcloud docker push $(PREFIX):$(RELEASE)

fmt:
	@echo "+ $@"
	@go list -f '{{if len .TestGoFiles}}"gofmt -s -l {{.Dir}}"{{end}}' $(shell go list ${PKG}/... | grep -v vendor) | xargs -L 1 sh -c

lint:
	@echo "+ $@"
	@go list -f '{{if len .TestGoFiles}}"golint {{.Dir}}/..."{{end}}' $(shell go list ${PKG}/... | grep -v vendor) | xargs -L 1 sh -c

test: fmt lint vet
	@echo "+ $@"
	@go test -v -race -tags "$(BUILDTAGS) cgo" $(shell go list ${PKG}/... | grep -v vendor)

cover:
	@echo "+ $@"
	@go list -f '{{if len .TestGoFiles}}"go test -coverprofile={{.Dir}}/.coverprofile {{.ImportPath}}"{{end}}' $(shell go list ${PKG}/... | grep -v vendor) | xargs -L 1 sh -c
	gover
	goveralls -coverprofile=gover.coverprofile -service travis-ci -repotoken ${COVERALLS_TOKEN}

vet:
	@echo "+ $@"
	@go vet $(shell go list ${PKG}/... | grep -v vendor)

clean:
	rm -f nginx-ingress-controller
