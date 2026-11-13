# Bygget sker i Go-imagen. Det som körs är bara binären, CA-certifikaten och två tomma kataloger.
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
# Databasen ligger i /data, en monterad katalog. Med read_only behöver /tmp vara en tmpfs.
COPY --from=build --chown=1000:1000 /out/data /data
# /tmp kopieras via sin förälder, så att rättigheterna (1777) följer med; en tmpfs ärver dem.
COPY --from=build /out/fs/ /
USER 1000:1000
# Statussidan (/status) för Homepage; nås bara inifrån nätverket.
EXPOSE 8080
# Processen skriver /tmp/masto-rss.alive varje minut. Är filen äldre än 3 minuter står loopen still.
HEALTHCHECK --interval=60s --timeout=5s --start-period=30s --retries=3 CMD ["/masto-rss", "healthcheck"]
ENTRYPOINT ["/masto-rss"]
