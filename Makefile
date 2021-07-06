up:
	docker-compose up -d

test:
	make up
	go test -count=1 -race ./...

monitor-redis:
	docker-compose exec redis redis-cli monitor

clear-redis:
	docker-compose exec redis redis-cli flushall