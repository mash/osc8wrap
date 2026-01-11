.PHONY: build test clean install release

build:
	go build -o osc8wrap .

test:
	go test -v ./...

clean:
	rm -f osc8wrap
	rm -rf dist

install:
	go install .

release:
ifndef VERSION
	$(error VERSION is required. Usage: make release VERSION=v0.1.0)
endif
	git tag -a $(VERSION) -m "Release $(VERSION)"
	git push origin $(VERSION)
	goreleaser release --clean
