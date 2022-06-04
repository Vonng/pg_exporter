#==============================================================#
# File      :   Makefile
# Mtime     :   2022-05-13
# Copyright (C) 2018-2022 Ruohang Feng
#==============================================================#

# Get Current Version
VERSION=v0.5.0
# VERSION=`cat exporter/global.go | grep -E 'var Version' | grep -Eo '[0-9.]+'`

# Release Dir
LINUX_DIR:=dist/$(VERSION)/pg_exporter_$(VERSION)_linux-amd64
DARWIN_DIR:=dist/$(VERSION)/pg_exporter_$(VERSION)_darwin-amd64
WINDOWS_DIR:=dist/$(VERSION)/pg_exporter_$(VERSION)_windows-amd64


###############################################################
#                        Shortcuts                            #
###############################################################
# build on current platform
build:
	go build -o pg_exporter

# build darwin release
build-darwin:
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -a -ldflags '-extldflags "-static"' -o pg_exporter
	upx -9 pg_exporter

# build linux release
build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -ldflags '-extldflags "-static"' -o pg_exporter
	upx -9 pg_exporter

clean:
	rm -rf pg_exporter


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
release: conf release-dir release-darwin release-linux rpm # release-windows

release-dir:
	mkdir -p dist/$(VERSION)

release-clean:
	rm -rf dist/$(VERSION)

release-darwin: clean build-darwin
	rm -rf $(DARWIN_DIR) && mkdir -p $(DARWIN_DIR)
	cp -r pg_exporter $(DARWIN_DIR)/pg_exporter
	cp -f pg_exporter.yml $(DARWIN_DIR)/pg_exporter.yml
	tar -czf dist/$(VERSION)/pg_exporter_$(VERSION)_darwin-amd64.tar.gz -C dist/$(VERSION) pg_exporter_$(VERSION)_darwin-amd64
	rm -rf $(DARWIN_DIR)

release-linux: clean build-linux
	rm -rf $(LINUX_DIR) && mkdir -p $(LINUX_DIR)
	cp -r pg_exporter $(LINUX_DIR)/pg_exporter
	cp -f pg_exporter.yml $(LINUX_DIR)/pg_exporter.yml
	tar -czf dist/$(VERSION)/pg_exporter_$(VERSION)_linux-amd64.tar.gz -C dist/$(VERSION) pg_exporter_$(VERSION)_linux-amd64
	rm -rf $(LINUX_DIR)

rpm:
	nfpm package --packager rpm
	nfpm package --packager deb
	mv *.rpm *.deb dist/$(VERSION)

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
	go run main.go --log.level=Info --config=pg_exporter.yml --auto-discovery

debug:
	go run main.go --log.level=Debug --config=pg_exporter.yml --auto-discovery

curl:
	curl localhost:9630/metrics | grep -v '#' | grep pg_

upload:
	./upload.sh

.PHONY: build clean build-darwin build-linux\
 release release-darwin release-linux release-windows docker\
 install uninstall debug curl upload
