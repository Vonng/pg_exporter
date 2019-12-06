VERSION=`cat pg_exporter.go | grep -E 'var Version' | grep -Eo '[0-9.]+'`

build:
	go build -o pg_exporter

clean:
	rm -rf bin/pg_exporter

release-darwin: clean
	GOOS=darwin GOARCH=amd64 go build -o pg_exporter
	upx pg_exporter
	tar -cf bin/pg_exporter_v$(VERSION)_darwin-amd64.tar.gz pg_exporter
	rm -rf pg_exporter

release-linux: clean
	GOOS=linux GOARCH=amd64 go build -o pg_exporter
	upx pg_exporter
	tar -cf bin/pg_exporter_v$(VERSION)_linux-amd64.tar.gz pg_exporter
	rm -rf pg_exporter

release-windows: clean
	GOOS=windows GOARCH=amd64 go build -o pg_exporter
	upx pg_exporter
	tar -cf bin/pg_exporter_v$(VERSION)_windows-amd64.tar.gz pg_exporter
	rm -rf pg_exporter

docker: clean
	CGO_ENABLED=0 GOOS=linux go build -a -ldflags '-extldflags "-static"' -o pg_exporter
	docker build -t pg_exporter .

run:
	go run pg_exporter.go --log-level=Debug

curl:
	curl localhost:8848/metrics | grep -v '#' | grep pg_

release: release-linux release-darwin release-windows

.PHONY: build clean release-linux release-darwin release-windows release docker run curl
