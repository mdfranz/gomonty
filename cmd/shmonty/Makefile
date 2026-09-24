.PHONY: build run vet fmt

# Builds to the repo top level (../../shmonty), matching `make shmonty`
# from the repo root — run this from within cmd/shmonty/ instead when
# you're already iterating here.
build:
	CGO_ENABLED=0 go build -o ../../shmonty .
	@echo "Built ../../shmonty — run it with ../../shmonty (or cd ../.. && ./shmonty)"

run:
	CGO_ENABLED=0 go run .

vet:
	CGO_ENABLED=0 go vet ./...

fmt:
	gofmt -l .
