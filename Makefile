BINARY ?= ses-smtpd-relay
DOCKER_REGISTRY ?= ghcr.io
GIT_USER ?= $(shell git remote get-url origin | sed 's/.*[:/]\([^/]*\)\/[^/]*\.git/\1/')
DOCKER_IMAGE_NAME ?= $(GIT_USER)/ses-smtpd-relay
DOCKER_TAG ?= latest
DOCKER_IMAGE ?= ${DOCKER_REGISTRY}/${DOCKER_IMAGE_NAME}:${DOCKER_TAG}
VERSION ?= $(shell git describe --long --tags --dirty --always)

$(BINARY): main.go go.sum
	CGO_ENABLED=0 go build \
		-ldflags "-X main.version=$(VERSION)"  \
		-o $@ $<

go.sum: go.mod
	go mod tidy

.PHONY: docker
docker:
	docker build --build-arg VERSION=$(VERSION) -t $(DOCKER_IMAGE) .

.PHONY: docker-amd64
docker-amd64:
	docker build --platform linux/amd64 --build-arg VERSION=$(VERSION) -t $(DOCKER_IMAGE) .

.PHONY: publish
publish: docker
	docker push $(DOCKER_IMAGE)

.PHONY: clean
clean:
	rm $(BINARY) || true
