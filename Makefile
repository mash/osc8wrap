.PHONY: build test clean install

build:
	go build -o osc8wrap .

test:
	go test -v ./...

clean:
	rm -f osc8wrap

install:
	go install .
