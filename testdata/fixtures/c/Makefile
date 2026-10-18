# Reviewed fixture build for J3. Binaries are written under build/ and are not
# committed. Tests compile the same sources with -g -O0 for stable stacks.
CC ?= cc
CFLAGS ?= -g -O0 -fno-omit-frame-pointer -Wall -Wextra
BUILD_DIR ?= build

SOURCES := clean.c double_free.c hang.c invalid_read.c invalid_write.c leak.c many_errors.c race.c sigsegv.c uninit.c use_after_free.c
BINARIES := $(addprefix $(BUILD_DIR)/,$(SOURCES:.c=))

.PHONY: all clean
all: $(BINARIES)

$(BUILD_DIR)/race: race.c
	@mkdir -p $(BUILD_DIR)
	$(CC) $(CFLAGS) -pthread -o $@ $<

$(BUILD_DIR)/%: %.c
	@mkdir -p $(BUILD_DIR)
	$(CC) $(CFLAGS) -o $@ $<

clean:
	rm -rf $(BUILD_DIR)
