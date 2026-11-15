# Developer targets for gokm itself. Users just need `go install .` (or
# `make`) and then `gokm init` / `gokm build`.
#
#   make                       build ./gokm
#   make test                  unit tests
#   make qemu [KVER=...]       build the examples and load/unload them in QEMU
#                              (KERNEL= picks the kernel image, default
#                              /boot/vmlinuz-linux; KVER must match it)

KVER   ?= $(shell file -bL $${KERNEL:-/boot/vmlinuz-linux} | sed -n 's/.*version \([^ ]*\).*/\1/p')

.PHONY: all gokm test qemu clean
all: gokm

gokm:
	go build -o gokm .

test:
	go vet ./...
	go test ./...

qemu: gokm
	./gokm build -kver $(KVER) examples/hello
	sh test/qemu/run.sh examples/hello/hello.ko >hello.log 2>&1; cat hello.log
	grep -q "TEST: PASS" hello.log
	./gokm build -kver $(KVER) -heap-size 256K examples/gcstress
	sh test/qemu/run.sh examples/gcstress/gcstress.ko >gcstress.log 2>&1; cat gcstress.log
	grep -q "TEST: PASS" gcstress.log && grep -q "gcstress: OK" gcstress.log && \
		grep -q "list still intact at exit" gcstress.log
	@rm -f hello.log gcstress.log
	@echo "all QEMU tests passed"

clean:
	rm -f gokm examples/*/*.ko
