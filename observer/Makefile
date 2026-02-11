.PHONY: test test-integration test-integration-up test-integration-down

# Run unit tests only
test:
	go test -count=1 ./...

# Start integration test dependencies (Redis only)
test-integration-up:
	docker-compose -f docker-compose.test.yml up -d
	@echo "Waiting for services to be ready..."
	@timeout 30 sh -c 'until docker-compose -f docker-compose.test.yml ps | grep -q "healthy"; do sleep 1; done' || true
	@sleep 2

# Stop integration test dependencies
test-integration-down:
	docker-compose -f docker-compose.test.yml down -v

# Run integration tests (requires Docker)
test-integration: test-integration-up
	@echo "Running integration tests..."
	REDIS_URL=redis://localhost:6379/0 go test -count=1 -tags=integration ./internal/services/storage/... || (make test-integration-down && exit 1)
	@make test-integration-down
	@echo "Integration tests completed successfully"
