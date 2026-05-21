.PHONY: all build-server build-client clean forge-build

# Default: compile both Go binaries
all: build-server build-client

build-server:
	go build -o bin/server ./cmd/server/

build-client:
	go build -o bin/client ./cmd/client/

# Build for common targets
build-client-linux-amd64:
	GOOS=linux GOARCH=amd64 go build -o bin/client-linux-amd64 ./cmd/client/

build-client-linux-arm64:
	GOOS=linux GOARCH=arm64 go build -o bin/client-linux-arm64 ./cmd/client/

build-client-windows-amd64:
	GOOS=windows GOARCH=amd64 go build -o bin/client-windows-amd64.exe ./cmd/client/

build-client-darwin-amd64:
	GOOS=darwin GOARCH=amd64 go build -o bin/client-darwin-amd64 ./cmd/client/

# Compile Solidity with Foundry
forge-build:
	forge build --contracts contracts/ --out out/

# Extract ABI and bytecode for embedding
abi-export:
	forge inspect C2Controller abi > contracts/C2Controller.abi
	forge inspect C2Controller bytecode > contracts/C2Controller.bin

# Tidy Go modules
tidy:
	go mod tidy

clean:
	rm -rf bin/ out/
