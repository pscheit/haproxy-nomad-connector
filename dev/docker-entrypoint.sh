#!/bin/sh
set -e

# Install socat for socket communication in tests
echo "Installing socat..."
apk add --no-cache socat

# Copy template to haproxy.cfg on first start
if [ ! -f /usr/local/etc/haproxy/haproxy.cfg ]; then
  echo "Initializing haproxy.cfg from template..."
  cp /usr/local/etc/haproxy/haproxy.cfg.template /usr/local/etc/haproxy/haproxy.cfg
fi

# Execute HAProxy
exec "$@"
