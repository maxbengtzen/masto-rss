# Built in the Go image. What runs is just the binary, the CA certificates and two empty directories.
FROM golang:1.27.4-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY VERSION version.go ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/masto-rss ./cmd/masto-rss \
 && mkdir -p /out/data /out/fs/tmp && chmod 1777 /out/fs/tmp

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/masto-rss /masto-rss
# The database lives in /data, a mounted directory. With read_only, /tmp needs to be a tmpfs.
COPY --from=build --chown=1000:1000 /out/data /data
# /tmp is copied via its parent so its permissions (1777) come along; a tmpfs inherits them.
COPY --from=build /out/fs/ /
USER 1000:1000
# The status page (/status) for Homepage; only reachable from inside the network.
EXPOSE 8080
# The process writes /tmp/masto-rss.alive every minute. If the file is older than 3 minutes, the loop has stalled.
HEALTHCHECK --interval=60s --timeout=5s --start-period=30s --retries=3 CMD ["/masto-rss", "healthcheck"]
ENTRYPOINT ["/masto-rss"]
