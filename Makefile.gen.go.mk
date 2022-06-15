# DO NOT EDIT. Generated with:
#
#    devctl@4.24.1
#

APPLICATION    := $(shell go list -m | cut -d '/' -f 3)
BUILDTIMESTAMP := $(shell date -u '+%FT%TZ')
GITSHA1        := $(shell git rev-parse --verify HEAD)
MODULE         := $(shell go list -m)
OS             := $(shell go env GOOS)
SOURCES        := $(shell find . -name '*.go')
VERSION        := $(shell architect project version)
ifeq ($(OS), linux)
EXTLDFLAGS := -static
endif
LDFLAGS        ?= -w -linkmode 'auto' -extldflags '$(EXTLDFLAGS)' \
  -X '$(shell go list -m)/pkg/project.buildTimestamp=${BUILDTIMESTAMP}' \
  -X '$(shell go list -m)/pkg/project.gitSHA=${GITSHA1}'

##@ Go

.PHONY: imports
imports: ## Runs goimports.
	@echo "====> $@"
	goimports -local $(MODULE) -w .

.PHONY: lint
lint: ## Runs golangci-lint.
	@echo "====> $@"
	golangci-lint run -E gosec -E goconst --timeout=15m ./...