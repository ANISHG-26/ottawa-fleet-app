ARG GO_VERSION=1.27.1
FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY services/go.mod services/go.sum ./services/
WORKDIR /src/services
RUN go mod download
COPY services/ ./
COPY db/ /src/db/
ARG APP_CMD
ARG BUILD_SERVICE=local
ARG BUILD_COMMIT=local
ARG BUILD_ARCH=unknown
RUN test -n "$APP_CMD" && CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags="-s -w -X github.com/ANISHG-26/ottawa-fleet-app/services/internal/buildinfo.Service=${BUILD_SERVICE} -X github.com/ANISHG-26/ottawa-fleet-app/services/internal/buildinfo.Commit=${BUILD_COMMIT} -X github.com/ANISHG-26/ottawa-fleet-app/services/internal/buildinfo.Architecture=${BUILD_ARCH}" \
    -o /out/app "./cmd/${APP_CMD}"

FROM alpine:3.22.1
RUN addgroup -S -g 10001 app && adduser -S -D -H -u 10001 -G app app \
    && apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build --chown=10001:10001 /out/app /app/app
COPY --from=build --chown=10001:10001 /src/db /app/db
ENV DB_MIGRATIONS_DIR=/app/db
USER 10001:10001
ENTRYPOINT ["/app/app"]
