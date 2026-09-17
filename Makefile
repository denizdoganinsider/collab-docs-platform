MODULES := ./gateway/... ./doc-service/...
TEST_DSN ?= root:root@tcp(localhost:3308)/docs_service_db?parseTime=true

.PHONY: test test-db e2e vet check

vet:
	gofmt -l gateway doc-service | tee /dev/stderr | test -z "$$(cat)"
	go vet $(MODULES)

test:
	go test $(MODULES) -race

# Database-backed tests (permission matrix) against the compose MySQL.
test-db:
	DOCS_TEST_DSN='$(TEST_DSN)' go test $(MODULES) -race -count=1

e2e:
	./scripts/e2e.sh

check: vet test-db e2e
