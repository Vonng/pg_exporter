#==============================================================#
# File      :   Makefile
# Mtime     :   2021-05-18
# Copyright (C) 2018-2021 Ruohang Feng
#==============================================================#

# Get Current Version
VERSION=0.4.1
# VERSION=`cat exporter/global.go | grep -E 'var Version' | grep -Eo '[0-9.]+'`

# Release Dir
LINUX_DIR:=bin/release/v$(VERSION)/pg_exporter_v$(VERSION)_linux-amd64
DARWIN_DIR:=bin/release/v$(VERSION)/pg_exporter_v$(VERSION)_darwin-amd64
WINDOWS_DIR:=bin/release/v$(VERSION)/pg_exporter_v$(VERSION)_windows-amd64


###############################################################
#                        Shortcuts                            #
###############################################################
build:
	go build -o pg_exporter

build2:
	CGO_ENABLED=0 GOmOS=darwin GOARCH=amd64 go build -a -ldflags '-extldflags "-static"' -o pg_exporter
	upx -9 pg_exporter

clean:
	rm -rf pg_exporter conf.tar.gz


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
	mkdir -p bin/release/v$(VERSION)

release-clean:
	rm -rf bin/release/v$(VERSION)

release-darwin:
	rm -rf $(DARWIN_DIR) && mkdir -p $(DARWIN_DIR)
	CGO_ENABLED=0 GOmOS=darwin GOARCH=amd64 go build -a -ldflags '-extldflags "-static"' -o $(DARWIN_DIR)/pg_exporter
	upx $(DARWIN_DIR)/pg_exporter
	cp -f pg_exporter.yml $(DARWIN_DIR)/pg_exporter.yml
	tar -czf bin/release/v$(VERSION)/pg_exporter_v$(VERSION)_darwin-amd64.tar.gz -C bin/release/v$(VERSION) pg_exporter_v$(VERSION)_darwin-amd64
	rm -rf $(DARWIN_DIR)

release-linux:
	rm -rf $(DARWIN_DIR) && mkdir -p $(LINUX_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -ldflags '-extldflags "-static"' -o $(LINUX_DIR)/pg_exporter
	upx $(LINUX_DIR)/pg_exporter
	cp -f pg_exporter.yml $(LINUX_DIR)/pg_exporter.yml
	cp -f service/pg_exporter.* $(LINUX_DIR)/
	tar -czf bin/release/v$(VERSION)/pg_exporter_v$(VERSION)_linux-amd64.tar.gz -C bin/release/v$(VERSION) pg_exporter_v$(VERSION)_linux-amd64
	rm -rf $(LINUX_DIR)

# build docker image
docker: linux
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -ldflags '-extldflags "-static"' -o pg_exporter
	docker build -t pg_exporter .

# build centos/redhat rpm package
rpm:
	./make-rpm.sh

release: clean conf release-linux release-darwin # release-windows


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

.PHONY: build clean release-linux release-darwin release-windows rpm release linux docker run curl conf upload
