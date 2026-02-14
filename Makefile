.PHONY: build test lint clean install release

build:
	go build -o osc8wrap .
	go build -o osc8wrap-replay ./cmd/osc8wrap-replay

test:
	go test -v ./...

lint:
	golangci-lint run

clean:
	rm -f osc8wrap
	rm -f osc8wrap-replay
	rm -rf dist

install:
	go install .
	go install ./cmd/osc8wrap-replay

release:
ifndef VERSION
	$(error VERSION is required. Usage: make release VERSION=v0.1.0)
endif
	git tag -a $(VERSION) -m "Release $(VERSION)"
	git push origin $(VERSION)
	goreleaser release --clean
