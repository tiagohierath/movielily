# movielily tasks — run `just` to list

default:
    @just --list

# build the CLI to ./bin/movielily
build:
    go build -o bin/movielily .

# run without building, e.g. `just run watch clip.mp4`
run *args:
    go run . {{args}}

# unit tests
test:
    go test ./...

# format + vet
fmt:
    gofmt -w .
    go vet ./...

lint:
    golangci-lint run

# refresh deps + vendor dir (vendor/ is what the flake builds from)
tidy:
    go mod tidy
    go mod vendor

clean:
    rm -rf bin
