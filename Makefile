.PHONY: help
## help: prints this help message
help:
	@echo "Usage: \n"
	@sed -n 's/^##//p' ${MAKEFILE_LIST} | column -t -s ':' |  sed -e 's/^/ /'

.PHONY: test
## test: run tests
test:
	@go test -cover ./...

.PHONY: lint
## lint: run golangci-lint
# Install: https://golangci-lint.run/usage/install/
lint:
	@golangci-lint run ./... --out-format colored-line-number

.PHONY: run
## run: run the thumbnailer
## : examples:
## :   make run arguments="--force-thumbnails --include=*/People"
run:
	@echo "Running thumbnailer..."
	@go run . $(arguments)
