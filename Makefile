# Variables
APP_NAME=reconcile-tfstate
BUILD_DIR=bin
MAIN_PATH=.

# Go build flags
# -s: Strip symbols (reduces binary size)
# -w: Omit DWARF debugging information
LDFLAGS=-ldflags "-s -w"

.PHONY: all mac-intel mac-silicon linux-arm linux clean summary

# Create build directory if it doesn't exist
$(BUILD_DIR):
	mkdir -p $(BUILD_DIR)

summary:
	@if command -v summarize > /dev/null; then \
		summarize -i "go,Makefile,mod"; \
	fi

# Build for all platforms
all: summary mac-intel mac-silicon linux linux-arm windows

# Build for macOS Intel (amd64)
mac-intel: $(BUILD_DIR) summary
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(APP_NAME)-darwin-amd64 $(MAIN_PATH)

# Build for macOS Silicon (arm64)
mac-silicon: $(BUILD_DIR) summary
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(APP_NAME)-darwin-arm64 $(MAIN_PATH)

# Build for Linux ARM64
linux-arm: $(BUILD_DIR) summary
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(APP_NAME)-linux-arm64 $(MAIN_PATH)

# Build for Linux AMD64
linux: $(BUILD_DIR) summary
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(APP_NAME)-linux-amd64 $(MAIN_PATH)

# Build for Windows AMD64
windows: $(BUILD_DIR) summary
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(APP_NAME).exe $(MAIN_PATH)

# Clean build artifacts
clean:
	rm -rf $(BUILD_DIR)
