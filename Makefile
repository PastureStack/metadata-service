.RECIPEPREFIX := >

TARGETS := ci build test validate verify-licenses check-build-downloads package

DAPPER_IMAGE ?= pasturestack-metadata-service-builder:ubuntu26
DAPPER_HOST_ARCH ?= amd64
DAPPER_SOURCE ?= /go/src/github.com/PastureStack/metadata-service
DOCKER_BUILD_NETWORK ?= host
UBUNTU_MIRROR ?= http://archive.ubuntu.com/ubuntu
BUILDX_BIN ?= /usr/libexec/docker/cli-plugins/docker-buildx
BUILDX_SHA256 ?= 5f42ff0a165e3834c4fd73a91b8d41c37a3c0a3475d0101cc13cfcf880ce5978
DOCKER_CLI_BIN ?= $(CURDIR)/dist/dependencies/docker-cli-29.6.2-linux-amd64
DOCKER_CLI_SHA256 ?= dda0804fca9b37a16e688356049ddf51fdd4c1a435c0a41055ec81cdf121535a

.dapper:
>test -x $(BUILDX_BIN)
>echo "$(BUILDX_SHA256)  $(BUILDX_BIN)" | sha256sum -c -
>test -x $(DOCKER_CLI_BIN)
>echo "$(DOCKER_CLI_SHA256)  $(DOCKER_CLI_BIN)" | sha256sum -c -
>docker build \
>  --network $(DOCKER_BUILD_NETWORK) \
>  --build-arg DAPPER_HOST_ARCH=$(DAPPER_HOST_ARCH) \
>  --build-arg UBUNTU_MIRROR=$(UBUNTU_MIRROR) \
>  -t $(DAPPER_IMAGE) \
>  -f Dockerfile.dapper .

$(TARGETS): .dapper
>docker run --rm \
>  -v $(CURDIR):$(DAPPER_SOURCE) \
>  -v /var/run/docker.sock:/var/run/docker.sock \
>  -v $(BUILDX_BIN):/usr/local/lib/docker/cli-plugins/docker-buildx:ro \
>  -v $(DOCKER_CLI_BIN):/usr/local/bin/docker:ro \
>  -e DAPPER_UID=$$(id -u) \
>  -e DAPPER_GID=$$(id -g) \
>  -e ARCH=$(DAPPER_HOST_ARCH) \
>  -e TAG \
>  -e REPO \
>  -e VERSION_OVERRIDE \
>  -e RELEASE_VERSION \
>  -e DOCKER_BUILD_NETWORK \
>  -e PASTURESTACK_PRIVATE_DENYLIST_FILE \
>  -e BUILDX_SHA256=$(BUILDX_SHA256) \
>  -e DOCKER_CLI_SHA256=$(DOCKER_CLI_SHA256) \
>  -e UBUNTU_MIRROR \
>  $(DAPPER_IMAGE) $@

.DEFAULT_GOAL := ci

.PHONY: .dapper $(TARGETS)
