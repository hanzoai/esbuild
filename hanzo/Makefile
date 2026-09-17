# The shipping lane for hanzoai/esbuild: the native binaries and the
# wasip1/wasm module, byte-for-byte reproducible, each artifact carrying the
# version it will answer to.
#
#   make -f hanzo/Makefile all        # run from the repository root
#   make -f hanzo/Makefile version    # print the version the artifacts carry
#
# Everything the fork adds lives under hanzo/, so a rebase onto evanw/esbuild
# touches none of it. Upstream's own Makefile is the development and npm lane
# and is left alone; this one produces the files the estate ships.
#
# What upstream's platform-* targets do that this does not: build a patched Go
# compiler (a 5-minute source build, to strip buildinfo) and run
# scripts/esbuild.js through node to stamp npm package.json files. Neither is
# needed to ship a .wasm to a Go host, and both put node and a compiler build
# in front of every artifact.

ifeq ($(wildcard cmd/esbuild/version.go),)
$(error run this from the repository root: make -f hanzo/Makefile <target>)
endif

# The version is read from the constant that is actually compiled in, not from
# version.txt. version.txt is the input to `node scripts/esbuild.js
# --update-version-go`; version.go is its output and the only string the binary
# will accept in `--service=`. A bump to version.txt that never ran the
# generator would otherwise name every artifact for a version it rejects.
VERSION := $(shell sed -n 's/^const esbuildVersion = "\(.*\)"$$/\1/p' cmd/esbuild/version.go)
COMMIT := $(shell git rev-parse HEAD 2>/dev/null || echo unknown)

# Pin the toolchain to upstream's own pin. Go honours an explicit GOTOOLCHAIN
# over anything in go.mod and fetches it once, so the same bytes come out of a
# laptop and out of the runner without the workflow having to install anything.
export GOTOOLCHAIN := go$(shell cat go.version)
export GOWORK := off
export CGO_ENABLED := 0

# -trimpath and -buildvcs=false keep the source path and the git state out of
# the output; -buildid= clears the one remaining nondeterministic field.
FLAGS := -trimpath -buildvcs=false "-ldflags=-s -w -buildid="

DIST := hanzo/dist
SRC := $(wildcard cmd/esbuild/*.go pkg/*/*.go internal/*/*/*.go internal/*/*.go) go.mod go.version

# The four hosts the estate runs on. esbuild itself supports far more; a lane
# that builds platforms nothing here executes only slows the release down.
NATIVE := \
	$(DIST)/esbuild-$(VERSION)-linux-amd64 \
	$(DIST)/esbuild-$(VERSION)-linux-arm64 \
	$(DIST)/esbuild-$(VERSION)-darwin-amd64 \
	$(DIST)/esbuild-$(VERSION)-darwin-arm64
WASM := $(DIST)/esbuild-$(VERSION)-wasip1-wasm.wasm
MANIFEST := $(DIST)/esbuild-$(VERSION).json

.PHONY: all native wasm manifest version clean
all: version manifest
native: $(NATIVE)
wasm: $(WASM)
manifest: $(MANIFEST)

# Every artifact name carries the version, so a file cannot be moved, renamed
# in a chart, or attached to a release without carrying it along. hanzo/stamp
# then proves the claim by making the artifact say its own version.
define artifact
$(DIST)/esbuild-$(VERSION)-$(1)-$(2)$(3): $(SRC) | $(DIST)
	@echo "  $(1)/$(2) -> $$@"
	@GOOS=$(1) GOARCH=$(2) go build $(FLAGS) -o $$@ ./cmd/esbuild
endef
$(eval $(call artifact,linux,amd64,))
$(eval $(call artifact,linux,arm64,))
$(eval $(call artifact,darwin,amd64,))
$(eval $(call artifact,darwin,arm64,))
$(eval $(call artifact,wasip1,wasm,.wasm))

# The manifest is written by running the artifacts, never by copying $(VERSION)
# into a file. See hanzo/stamp.
$(MANIFEST): $(NATIVE) $(WASM) hanzo/stamp/main.go
	@cd hanzo/stamp && go run . -dist ../dist -version $(VERSION) -commit $(COMMIT) -out ../dist/esbuild-$(VERSION).json

version:
	@test -n "$(VERSION)" || { echo "no esbuildVersion in cmd/esbuild/version.go"; exit 1; }
	@test "$(VERSION)" = "$$(cat version.txt)" || { \
		echo "version.txt says $$(cat version.txt) but cmd/esbuild/version.go says $(VERSION)"; \
		echo "run: node scripts/esbuild.js --update-version-go"; exit 1; }
	@echo $(VERSION)

$(DIST):
	@mkdir -p $(DIST)

clean:
	rm -rf $(DIST)
