set -e

cat > /tmp/custom-golangci.yml << EOF
linters:
  disable-all: true
  enable:
    - errcheck
    - gosimple
    - govet
    - ineffassign
    - staticcheck
    - unused
    - gocyclo
    - gofmt
    - goimports
    - revive
    - unconvert
    - unparam
    - whitespace
EOF

$(go env GOPATH)/bin/golangci-lint run --config=/tmp/custom-golangci.yml --out-format=colored-line-number ./...
