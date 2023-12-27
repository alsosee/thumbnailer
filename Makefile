.PHONY: help
## help: prints this help message
help:
	@echo "Usage: \n"
	@sed -n 's/^##//p' ${MAKEFILE_LIST} | column -t -s ':' |  sed -e 's/^/ /'

.PHONY: run
## run: run the thumbnailer
## : examples:
## :   make run arguments="--force-thumbnails --include=*/People"
run:
	@echo "Running thumbnailer..."
	@go run . $(arguments)
