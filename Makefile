all: push

BUILDTAGS=

# building inside travis generates a custom version of the
# backends in order to run e2e tests agains the build.
ifdef TRAVIS_BUILD_ID
  RELEASE := ci-build-${TRAVIS_BUILD_ID}
endif

# 0.0 shouldn't clobber any release builds
RELEASE?=0.0
PREFIX?=quay.io/aledbf/nginx-ingress-controller
# by default build a linux version
GOOS?=linux

REPO_INFO=$(shell git config --get remote.origin.url)

ifndef COMMIT
  COMMIT := git-$(shell git rev-parse --short HEAD)
endif

# base package. It contains the common and backends code
PKG := "github.com/aledbf/ingress-controller"

GO_LIST_FILES=$(shell go list ${PKG}/... | grep -v vendor)

# Checks if go code follow formtat rules using gofmt.
#
# Example:
#   make fmt
.PHONY: fmt
fmt:
	@echo "+ $@"
	@go list -f '{{if len .TestGoFiles}}"gofmt -s -l {{.Dir}}"{{end}}' ${GO_LIST_FILES} | xargs -L 1 sh -c

# Checks if go code using go lint.
#
# Example:
#   make lint
.PHONY: lint
lint:
	@echo "+ $@"
	@go list -f '{{if len .TestGoFiles}}"golint {{.Dir}}/..."{{end}}' ${GO_LIST_FILES} | xargs -L 1 sh -c

# Build and run tests.
#
# Example:
#   make test-e2e
.PHONY: test
test: fmt lint vet
	@echo "+ $@"
	@go test -v -race -tags "$(BUILDTAGS) cgo" ${GO_LIST_FILES}

# Build and run end-to-end tests.
#
# Example:
#   make test-e2e
.PHONY: test-e2e
test-e2e: ginkgo
	@go run hack/e2e.go -v --up --test --down

.PHONY: cover
cover:
	@echo "+ $@"
	@go list -f '{{if len .TestGoFiles}}"go test -coverprofile={{.Dir}}/.coverprofile {{.ImportPath}}"{{end}}' ${GO_LIST_FILES} | xargs -L 1 sh -c
	gover
	goveralls -coverprofile=gover.coverprofile -service travis-ci -repotoken ${COVERALLS_TOKEN}

.PHONY: vet
vet:
	@echo "+ $@"
	@go vet ${GO_LIST_FILES}

.PHONY: clean
clean:
	make -C backends/nginx clean

.PHONY: backends
backends:
	make -C backends/nginx build

.PHONY: backends-images
backends-images:
	make -C backends/nginx container

.PHONY: backends-push
backends-push:
	make -C backends/nginx push

# Build ginkgo
#
# Example:
# make ginkgo
.PHONY: ginkgo
ginkgo:
	go get github.com/onsi/ginkgo/ginkgo
