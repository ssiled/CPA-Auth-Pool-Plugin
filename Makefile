.PHONY: test build-linux

test:
	go test ./...

build-linux:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=1 go build -buildmode=c-shared -o dist/cpa-auth-pool_linux_amd64.so ./cmd/cpa-auth-pool
	GOOS=linux GOARCH=arm64 CGO_ENABLED=1 go build -buildmode=c-shared -o dist/cpa-auth-pool_linux_arm64.so ./cmd/cpa-auth-pool
