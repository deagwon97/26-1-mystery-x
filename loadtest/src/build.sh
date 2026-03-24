#!/bin/bash
set -e

# go.mod 이 없으면 초기화
if [ ! -f go.mod ]; then
  go mod init benchmark
fi

go build -tags genfiles -o create-test-files .
go build -o benchmark .

echo "Done: ./create-test-files, ./benchmark"
