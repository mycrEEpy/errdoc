snapshot-single: clean
	unset GITLAB_TOKEN && goreleaser build --snapshot --single-target

snapshot: clean
	goreleaser build --snapshot

build: clean
	mkdir -p dist
	go build -v -o dist/errdoc ./cmd/errdoc/

release: clean
	goreleaser release --clean

install: clean
	go install ./cmd/errdoc

lint:
	golangci-lint run --timeout 5m

test:
	go test -v -coverprofile coverage.out -race ./...

tidy:
	go mod tidy

fmt:
	go fmt ./...

clean:
	go clean
	rm -rf dist/
