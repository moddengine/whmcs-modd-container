#!/bin/sh
set -eu

binary=${BINARY:-controller}

check() {
    if ! go version -m "$binary" 2>/dev/null | grep -q 'CGO_ENABLED=0'; then
        echo "ERROR: $binary is dynamically linked and may fail with 'required file not found'; rebuild with './build.sh'." >&2
        exit 1
    fi
    echo "$binary: portable static Go binary"
}

case ${1:-build} in
    build)
        CGO_ENABLED=0 go build -buildvcs=false -trimpath \
            -ldflags "-X main.version=${VERSION:-dev} -X main.commit=${COMMIT:-unknown} -X main.buildDate=${BUILD_DATE:-unknown}" \
            -o "$binary" ./cmd/controller
        check
        ;;
    check)
        check
        ;;
    test)
        go test ./...
        ;;
    *)
        echo "Usage: $0 [build|check|test]" >&2
        exit 2
        ;;
esac

