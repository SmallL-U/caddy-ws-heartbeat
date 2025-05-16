ARG version=2.9-alpine
ARG builderVersion=2.9-builder-alpine
FROM caddy:$builderVersion AS builder

RUN go env -w GO111MODULE=on \
    && xcaddy build \
    --with github.com/smalll-u/caddy-ws-heartbeat \
    --with github.com/caddyserver/replace-response

FROM caddy:$version
COPY --from=builder /usr/bin/caddy /usr/bin/caddy