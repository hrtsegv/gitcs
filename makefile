# Makefile for building Go tool for different platforms and compressing binaries

# Binary name
BINARY_NAME=gitcs

# Output directory
OUTPUT_DIR=bin

# Cross-compilation targets
PLATFORMS=linux/amd64 linux/arm64 linux/386 windows/amd64 windows/arm64 windows/386 darwin/amd64 darwin/arm64

# Build command for each platform
build:
	@set -e; mkdir -p "$(OUTPUT_DIR)"; for platform in $(PLATFORMS); do \
		export GOOS=$${platform%/*}; \
		export GOARCH=$${platform#*/}; \
		output_name=$(OUTPUT_DIR)/$(BINARY_NAME)_$${GOOS}_$${GOARCH}; \
		if [ $$GOOS = "windows" ]; then output_name=$$output_name.exe; fi; \
		echo "Building $$output_name"; \
		CGO_ENABLED=0 go build -o "$$output_name"; \
	done

# Compress binaries into a zip file
compress:
	zip -j $(OUTPUT_DIR)/$(BINARY_NAME)_binaries.zip $(OUTPUT_DIR)/*

# Clean the binaries and zip file
clean:
	rm -rf $(OUTPUT_DIR)/*

# Build and compress
all: clean build compress

.PHONY: build compress clean all
