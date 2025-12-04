# HAProxy with DataPlane API for testing
# The base image no longer includes dataplaneapi, so we add it ourselves
FROM haproxytech/haproxy-alpine:3.0

# Download and install the DataPlane API from the official Alpine package
ARG DATAPLANEAPI_VERSION=3.2.7
RUN apk add --no-cache curl socat && \
    curl -LO "https://github.com/haproxytech/dataplaneapi/releases/download/v${DATAPLANEAPI_VERSION}/dataplaneapi_${DATAPLANEAPI_VERSION}_linux_amd64.apk" && \
    apk add --no-cache --allow-untrusted dataplaneapi_${DATAPLANEAPI_VERSION}_linux_amd64.apk && \
    rm -f dataplaneapi_${DATAPLANEAPI_VERSION}_linux_amd64.apk && \
    ln -sf /usr/local/bin/dataplaneapi /usr/bin/dataplaneapi && \
    apk del curl

# Copy entrypoint script
COPY docker-entrypoint.sh /usr/local/etc/haproxy/docker-entrypoint.sh
RUN chmod +x /usr/local/etc/haproxy/docker-entrypoint.sh

ENTRYPOINT ["/usr/local/etc/haproxy/docker-entrypoint.sh"]
CMD ["haproxy", "-W", "-db", "-f", "/usr/local/etc/haproxy/haproxy.cfg"]
