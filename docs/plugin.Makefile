# Reference Makefile for a goclaw plugin repo.
#
# Copy this into your plugin repo and set NAME to your plugin's name (== plugin.yml
# `name` == the built binary). Self-contained: no dependency on goclawkit at release
# time. Targets:
#
#   make build              host-platform binary (for -selftest / local use)
#   make build-linux        Linux/amd64 binary you install into goclaw
#   make selftest           build + run the plugin's -selftest
#
#   make bump VERSION=1.3.0 edit plugin.yml `version` to 1.3.0 and commit it
#   make release            tag a release of the version ALREADY in plugin.yml
#   make release PUSH=1     ... and push the tag
#
# The version of record is plugin.yml. `release` does NOT bump, it tags whatever
# plugin.yml currently says, after checking the binary agrees (the SDK's -version flag).
# So a repo whose plugin.yml is already at 1.0.0 but never tagged just runs `make
# release` to cut v1.0.0. To ship a new number, `make bump VERSION=x` first, then
# `make release`.

NAME    := myplugin
GOOS    ?= linux
GOARCH  ?= amd64

.PHONY: build build-linux selftest bump release

build:
	go build -o $(NAME) .

build-linux:
	GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=0 go build -o $(NAME) .

selftest: build
	./$(NAME) -selftest

# bump VERSION=x.y.z : set plugin.yml version and commit (does NOT tag).
bump:
	@if [ -z "$(VERSION)" ]; then echo "ERROR: set VERSION, e.g. make bump VERSION=1.3.0"; exit 1; fi; \
	echo "$(VERSION)" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$$' || { echo "ERROR: VERSION must be semver MAJOR.MINOR.PATCH (no 'v')"; exit 1; }; \
	test -z "$$(git status --porcelain)" || { echo "ERROR: working tree not clean; commit or stash first"; exit 1; }; \
	sed -i.bak -E 's/^version:.*/version: "$(VERSION)"/' plugin.yml && rm -f plugin.yml.bak; \
	git add plugin.yml && git commit -q -m "bump version to $(VERSION)"; \
	echo "plugin.yml -> $(VERSION) (committed). Now: make release"

# release : tag v<version-in-plugin.yml> (no bump). PUSH=1 also pushes the tag.
release:
	@ver=$$(grep '^version:' plugin.yml | head -1 | tr -d '" ' | sed 's/version://; s/#.*//'); \
	test -n "$$ver" || { echo "ERROR: could not read version from plugin.yml"; exit 1; }; \
	test -z "$$(git status --porcelain)" || { echo "ERROR: working tree not clean; commit plugin.yml first"; exit 1; }; \
	if git rev-parse "v$$ver" >/dev/null 2>&1; then echo "ERROR: tag v$$ver already exists (bump first: make bump VERSION=...)"; exit 1; fi; \
	go build -o $(NAME) . || exit 1; \
	got=$$(./$(NAME) -version); \
	if [ "$$got" != "$$ver" ]; then echo "ERROR: binary -version ($$got) != plugin.yml ($$ver); fix the version in your code"; exit 1; fi; \
	echo "verified: plugin.yml = binary -version = $$ver"; \
	git tag -a "v$$ver" -m "v$$ver"; \
	echo "tagged v$$ver"; \
	if [ "$(PUSH)" = "1" ]; then \
	  git push origin "v$$ver" && echo "pushed tag v$$ver"; \
	else \
	  echo "not pushed. run: git push origin v$$ver"; \
	fi
