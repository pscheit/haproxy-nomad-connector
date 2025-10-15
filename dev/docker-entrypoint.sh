#!/bin/sh
set -e

# Install socat for socket communication in tests (apk caches it, very fast)
echo "Installing socat..."
apk add --no-cache socat

# ALWAYS reset haproxy.cfg from template to ensure clean slate for tests
# This allows setupCleanSlate() to simply restart the container
echo "Resetting haproxy.cfg from template (clean baseline for tests)..."
cp /usr/local/etc/haproxy/haproxy.cfg.template /usr/local/etc/haproxy/haproxy.cfg

# Execute HAProxy
exec "$@"
