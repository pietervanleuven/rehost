BINARY := rehost

.PHONY: build test vet lint tidy snapshot clean

build:
	go build -o $(BINARY) ./cmd/rehost

test:
	go test -race ./...

vet:
	go vet ./...

lint:
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint not installed: https://golangci-lint.run/welcome/install/"; exit 1; }
	golangci-lint run

tidy:
	go mod tidy

snapshot:
	@command -v goreleaser >/dev/null 2>&1 || { echo "goreleaser not installed: https://goreleaser.com/install/"; exit 1; }
	goreleaser release --snapshot --clean

clean:
	rm -rf $(BINARY) dist/
