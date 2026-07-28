binary := "kube-port-forward"
output_dir := "dist"

default: build

# Build for the current platform.
build:
    mkdir -p {{output_dir}}
    go build -o {{output_dir}}/{{binary}} .

# Build for macOS on Apple Silicon.
build-macos:
    mkdir -p {{output_dir}}
    CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o {{output_dir}}/{{binary}}-darwin-arm64 .

# Build for Linux on AMD64.
build-linux:
    mkdir -p {{output_dir}}
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o {{output_dir}}/{{binary}}-linux-amd64 .

# Build for Windows on AMD64.
build-win:
    mkdir -p {{output_dir}}
    CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o {{output_dir}}/{{binary}}-windows-amd64.exe .
