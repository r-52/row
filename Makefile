ROW_PG_DSN ?= postgres://row:row@localhost:55432/row_test?sslmode=disable

.PHONY: test test-pg pg-up pg-down fuzz bench vet lint

test:
	go vet ./...
	go test -race ./...

test-pg: pg-up
	ROW_PG_DSN="$(ROW_PG_DSN)" ROW_PG_REQUIRED=1 go test -race ./...

pg-up:
	docker compose up -d --wait

pg-down:
	docker compose down -v

fuzz:
	go test -run=XXX -fuzz=FuzzLex -fuzztime=60s .

bench:
	go test -bench=. -benchmem ./...

vet:
	go vet ./...
