test:
	docker compose up -d
	go test -count=1 -race ./...
	docker compose down

monitor-redis:
	docker compose exec redis redis-cli monitor

clear-redis:
	docker compose exec redis redis-cli flushall
