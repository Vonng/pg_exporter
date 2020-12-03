VERSION=`cat pg_exporter.go | grep -E 'var Version' | grep -Eo '[0-9.]+'`

build:
	go build -o pg_exporter

clean:
	rm -rf pg_exporter conf.tar.gz

release-darwin:
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build  -a -ldflags '-extldflags "-static"' -o pg_exporter
	upx pg_exporter
	mkdir -p bin/pg_exporter_v$(VERSION)_darwin-amd64
	mv -f pg_exporter bin/pg_exporter_v$(VERSION)_darwin-amd64/
	cp -f pg_exporter.yaml bin/pg_exporter_v$(VERSION)_darwin-amd64/
	tar -czf bin/pg_exporter_v$(VERSION)_darwin-amd64.tar.gz -C bin pg_exporter_v$(VERSION)_darwin-amd64
	rm -rf bin/pg_exporter_v$(VERSION)_darwin-amd64

release-linux: clean
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build  -a -ldflags '-extldflags "-static"' -o pg_exporter
	upx pg_exporter
	mkdir -p bin/pg_exporter_v$(VERSION)_linux-amd64
	mv -f pg_exporter bin/pg_exporter_v$(VERSION)_linux-amd64/
	cp -f pg_exporter.yaml bin/pg_exporter_v$(VERSION)_linux-amd64/
	cp -f service/pg_exporter.default bin/pg_exporter_v$(VERSION)_linux-amd64/
	cp -f service/pg_exporter.service bin/pg_exporter_v$(VERSION)_linux-amd64/
	tar -czf bin/pg_exporter_v$(VERSION)_linux-amd64.tar.gz -C bin pg_exporter_v$(VERSION)_linux-amd64
	rm -rf bin/pg_exporter_v$(VERSION)_linux-amd64

release-windows: clean
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build  -a -ldflags '-extldflags "-static"' -o pg_exporter
	upx pg_exporter
	mkdir -p bin/pg_exporter_v$(VERSION)_windows-amd64
	mv -f pg_exporter bin/pg_exporter_v$(VERSION)_windows-amd64/
	cp -f pg_exporter.yaml bin/pg_exporter_v$(VERSION)_windows-amd64/
	tar -czf bin/pg_exporter_v$(VERSION)_windows-amd64.tar.gz -C bin pg_exporter_v$(VERSION)_windows-amd64
	rm -rf bin/pg_exporter_v$(VERSION)_windows-amd64

rpm:
	./make-rpm.sh

install: build
	sudo install -m 0755 pg_exporter /usr/bin/pg_exporter

uninstall:
	sudo rm -rf /usr/bin/pg_exporter

conf:
	rm -rf pg_exporter.yaml
	cat conf/*.yaml >> pg_exporter.yaml

docker: linux
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -ldflags '-extldflags "-static"' -o pg_exporter
	docker build -t pg_exporter .

run:
	go run pg_exporter.go --log.level=Debug --config=conf

curl:
	curl localhost:9630/metrics | grep -v '#' | grep pg_

upload:
	./upload.sh

release: clean conf release-linux release-darwin # release-windows


.PHONY: build clean release-linux release-darwin release-windows rpm release linux docker run curl conf upload
