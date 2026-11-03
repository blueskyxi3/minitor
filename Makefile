PLATFORM ?= $(shell go env GOOS)-$(shell go env GOARCH)

.PHONY: build test lint frontend image
build:
	python3 scripts/build.py --platform $(PLATFORM)

frontend:
	npm ci --ignore-scripts --no-audit --no-fund
	npm run build

test: frontend
	go test -race ./internal/... .
	npm test

lint:
	npm run lint
	go vet ./internal/... .

image:
	docker build -t ai-hub-monitor:0.0.1 .
