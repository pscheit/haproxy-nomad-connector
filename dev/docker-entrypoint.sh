#!/bin/sh
set -e

# ALWAYS reset haproxy.cfg from template to ensure clean slate for tests
# This allows setupCleanSlate() to simply restart the container
echo "Resetting haproxy.cfg from template (clean baseline for tests)..."
cp /usr/local/etc/haproxy/haproxy.cfg.template /usr/local/etc/haproxy/haproxy.cfg

# Execute HAProxy
exec "$@"
