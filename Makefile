.PHONY: monitor monitor-stop

monitor:
	@python3 scripts/monitor_control.py start

monitor-stop:
	@python3 scripts/monitor_control.py stop

.PHONY: test-quorum-failover
test-quorum-failover:
	docker compose -p gptadmin-quorum-$$$$ -f tests/e2e/failover/docker-compose.yml \
		-f tests/e2e/node/compose.quorum.yml run --build --rm --no-deps -T quorum-e2e
