#==============================================================#
# File      :   Makefile
# Mtime     :   2025-02-14
# License   :   Apache-2.0 @ https://github.com/Vonng/pg_exporter
# Copyright :   2018-2025  Ruohang Feng / Vonng (rh@vonng.com)
#==============================================================#

# Get Current Version
VERSION=v0.8.0
# VERSION=`cat exporter/global.go | grep -E 'var Version' | grep -Eo '[0-9.]+'`

# Release Dir
LINUX_AMD_DIR:=dist/$(VERSION)/pg_exporter-$(VERSION).linux-amd64
LINUX_ARM_DIR:=dist/$(VERSION)/pg_exporter-$(VERSION).linux-arm64
DARWIN_AMD_DIR:=dist/$(VERSION)/pg_exporter-$(VERSION).darwin-amd64
DARWIN_ARM_DIR:=dist/$(VERSION)/pg_exporter-$(VERSION).darwin-arm64
WINDOWS_DIR:=dist/$(VERSION)/pg_exporter-$(VERSION).windows-amd64


###############################################################
#                        Shortcuts                            #
###############################################################
build:
	go build -o pg_exporter
clean:
	rm -rf pg_exporter
build-darwin-amd64:
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -a -ldflags '-extldflags "-static"' -o pg_exporter
build-darwin-arm64:
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -a -ldflags '-extldflags "-static"' -o pg_exporter
build-linux-amd64:
	CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 go build -a -ldflags '-extldflags "-static"' -o pg_exporter
build-linux-arm64:
	CGO_ENABLED=0 GOOS=linux  GOARCH=arm64 go build -a -ldflags '-extldflags "-static"' -o pg_exporter

release: release-linux release-darwin

release-linux: linux-amd64 linux-arm64
linux-amd64: clean build-linux-amd64
	rm -rf $(LINUX_AMD_DIR) && mkdir -p $(LINUX_AMD_DIR)
	nfpm package --packager rpm --config package/nfpm-amd64-rpm.yaml     --target dist/$(VERSION)
	nfpm package --packager deb --config package/nfpm-amd64-deb.yaml --target dist/$(VERSION)
	cp -r pg_exporter $(LINUX_AMD_DIR)/pg_exporter
	cp -f package/pg_exporter.yml $(LINUX_AMD_DIR)/pg_exporter.yml
	tar -czf dist/$(VERSION)/pg_exporter-$(VERSION).linux-amd64.tar.gz -C dist/$(VERSION) pg_exporter-$(VERSION).linux-amd64
	rm -rf $(LINUX_AMD_DIR)

linux-arm64: clean build-linux-arm64
	rm -rf $(LINUX_ARM_DIR) && mkdir -p $(LINUX_ARM_DIR)
	nfpm package --packager rpm --config package/nfpm-arm64-rpm.yaml     --target dist/$(VERSION)
	nfpm package --packager deb --config package/nfpm-arm64-deb.yaml --target dist/$(VERSION)
	cp -r pg_exporter $(LINUX_ARM_DIR)/pg_exporter
	cp -f package/pg_exporter.yml $(LINUX_ARM_DIR)/pg_exporter.yml
	tar -czf dist/$(VERSION)/pg_exporter-$(VERSION).linux-arm64.tar.gz -C dist/$(VERSION) pg_exporter-$(VERSION).linux-arm64
	rm -rf $(LINUX_ARM_DIR)

release-darwin: darwin-amd64 darwin-arm64
darwin-amd64: clean build-darwin-amd64
	rm -rf $(DARWIN_AMD_DIR) && mkdir -p $(DARWIN_AMD_DIR)
	cp -r pg_exporter $(DARWIN_AMD_DIR)/pg_exporter
	cp -f package/pg_exporter.yml $(DARWIN_AMD_DIR)/pg_exporter.yml
	tar -czf dist/$(VERSION)/pg_exporter-$(VERSION).darwin-amd64.tar.gz -C dist/$(VERSION) pg_exporter-$(VERSION).darwin-amd64
	rm -rf $(DARWIN_AMD_DIR)

darwin-arm64: clean build-darwin-arm64
	rm -rf $(DARWIN_ARM_DIR) && mkdir -p $(DARWIN_ARM_DIR)
	cp -r pg_exporter $(DARWIN_ARM_DIR)/pg_exporter
	cp -f package/pg_exporter.yml $(DARWIN_ARM_DIR)/pg_exporter.yml
	tar -czf dist/$(VERSION)/pg_exporter-$(VERSION).darwin-arm64.tar.gz -C dist/$(VERSION) pg_exporter-$(VERSION).darwin-arm64
	rm -rf $(DARWIN_ARM_DIR)



###############################################################
#                      Configuration                          #
###############################################################
# generate merged config from separated configuration
conf:
	rm -rf pg_exporter.yml
	cat config/collector/*.yml >> pg_exporter.yml


###############################################################
#                         Release                             #
###############################################################
release-dir:
	mkdir -p dist/$(VERSION)

release-clean:
	rm -rf dist/$(VERSION)

# build docker image
docker: build-linux docker-build
docker-build:
	docker build -t vonng/pg_exporter .
	docker image tag vonng/pg_exporter vonng/pg_exporter:$(VERSION)
	docker image push --all-tags vonng/pg_exporter

###############################################################
#                         Develop                             #
###############################################################
install: build
	sudo install -m 0755 pg_exporter /usr/bin/pg_exporter

uninstall:
	sudo rm -rf /usr/bin/pg_exporter

run:
	go run main.go --log.level=info --config=pg_exporter.yml --auto-discovery

debug:
	go run main.go --log.level=debug --config=pg_exporter.yml --auto-discovery

curl:
	curl localhost:9630/metrics | grep -v '#' | grep pg_

upload:
	./upload.sh

.PHONY: build clean build-darwin build-linux\
 release release-darwin release-linux release-windows docker\
 install uninstall debug curl upload
