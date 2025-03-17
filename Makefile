.PHONY: install
install:
	chmod +x ./bash/install && ./bash/install

.PHONY: build
build:
	./bash/build

.PHONY: restart
restart:
	./bash/restart

.PHONY: run
run:
	./bash/run

.PHONY: stop
stop:
	./bash/stop
