BINARY := rehost

.PHONY: build test vet lint tidy snapshot integration integration-clean clean

build:
	go build -o $(BINARY) ./cmd/rehost

test:
	go test -race ./...

vet:
	go vet ./...

# Runs the host-facing plumbing against a real sshd and a real database in a
# container. Needs Docker; excluded from `make test` by a build tag because it
# is slow and needs a daemon. See test/integration/README.md.
integration:
	@command -v docker >/dev/null 2>&1 || { echo "docker not installed or not on PATH"; exit 1; }
	go test -tags integration -count=1 -timeout 30m -v ./test/integration/

integration-clean:
	-docker ps -aq --filter "name=rehost-rig-" | xargs -r docker rm -f
	-docker rmi -f rehost-integration-rig

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
