FROM golang:1.23-bookworm AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test ./... && \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/lindblad-route-planner-cloud .

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates gosu && \
    rm -rf /var/lib/apt/lists/* && \
    useradd --system --uid 10001 --home-dir /nonexistent --shell /usr/sbin/nologin lrp && \
    mkdir -p /app /data && chown -R lrp:lrp /app /data
COPY --from=build /out/lindblad-route-planner-cloud /app/lindblad-route-planner-cloud
COPY render-entrypoint.sh /app/render-entrypoint.sh
RUN chmod 0755 /app/lindblad-route-planner-cloud /app/render-entrypoint.sh
WORKDIR /app
EXPOSE 10000
ENTRYPOINT ["/app/render-entrypoint.sh"]
CMD ["--public","--no-browser"]
