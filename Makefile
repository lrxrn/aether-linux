PREFIX ?= $(HOME)/.local

.PHONY: build install udev test
build:
	go build -o aetherctl .
install: build
	install -Dm755 aetherctl $(PREFIX)/bin/aetherctl
udev:
	sudo install -Dm644 udev/99-voicsky.rules /etc/udev/rules.d/99-voicsky.rules
	sudo udevadm control --reload
	sudo udevadm trigger --subsystem-match=hidraw --action=add
test:
	go test ./...
