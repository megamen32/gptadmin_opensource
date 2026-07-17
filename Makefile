.PHONY: monitor monitor-stop

monitor:
	@python3 scripts/monitor_control.py start

monitor-stop:
	@python3 scripts/monitor_control.py stop
