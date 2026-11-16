.PHONY: test build integration vet
build:
	go build -trimpath -o bin/helpdesk ./cmd/server
test:
	go test -race ./...
vet:
	go vet ./...
integration:
	@test -n "$$TEST_DATABASE_URL" || (echo 'Set TEST_DATABASE_URL to an empty disposable PostgreSQL database'; exit 1)
	go test -race -count=1 ./...
