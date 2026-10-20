# bezhook build/dist/deploy. Fleet frps is pinned to frp v0.64.0 — keep the
# go.mod pin in sync with /etc/frp on the gateways when upgrading.

VERSION ?= $(shell grep -m1 'var version' cmd/bezhook/main.go | grep -oE '[0-9]+\.[0-9]+\.[0-9]+')
TARGETS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64
DIST := dist
MIRROR_HOST ?= relay02.bezrabotnyi.com
MIRROR_DIR ?= /var/www/bezhook

.PHONY: build test vet dist checksums deploy clean

build:
	go build -o bezhook-bin ./cmd/bezhook

test:
	go test ./...

vet:
	go vet ./...

dist: clean-dist checksums

# Cross-compile every target; windows gets an .exe suffix.
dist-only:
	@mkdir -p $(DIST)/dl
	@for t in $(TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; \
		ext=""; [ "$$os" = "windows" ] && ext=".exe"; \
		echo "GOOS=$$os GOARCH=$$arch -> $(DIST)/dl/bezhook-$$os-$$arch$$ext"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			go build -trimpath -ldflags "-s -w" \
			-o $(DIST)/dl/bezhook-$$os-$$arch$$ext ./cmd/bezhook || exit 1; \
	done
	@cp install/install.sh $(DIST)/install.sh
	@cp install/install.ps1 $(DIST)/install.ps1
	@echo "$(VERSION)" > $(DIST)/dl/VERSION
	@cp README.md $(DIST)/

checksums:
	cd $(DIST)/dl && sha256sum bezhook-* > SHA256SUMS

deploy: dist
	rsync -av --delete $(DIST)/dl/ root@$(MIRROR_HOST):$(MIRROR_DIR)/dl/
	rsync -av $(DIST)/install.sh $(DIST)/install.ps1 $(DIST)/README.md root@$(MIRROR_HOST):$(MIRROR_DIR)/

clean-dist:
	rm -rf $(DIST)
	@$(MAKE) --no-print-directory dist-only

clean:
	rm -rf $(DIST) bezhook-bin
