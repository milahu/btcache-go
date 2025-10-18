#!/bin/sh

set -eux

go build ./cmd/btcache
go build ./cmd/test-client
