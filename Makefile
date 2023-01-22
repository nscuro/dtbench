LDFLAGS="-s -w"

build:
	mkdir -p ./bin
	CGO_ENABLED=0 go build -v -ldflags=${LDFLAGS} -o ./bin/dtbench
.PHONY: build

install:
	CGO_ENABLED=0 go install -v -ldflags=${LDFLAGS}
.PHONY: install

test:
	go test -v -cover ./...
.PHONY: test

lint:
	golangci-lint run
.PHONY: lint

clean:
	rm -rf ./bin
	go clean -testcache ./...
.PHONY: clean

all: clean build test
.PHONY: all