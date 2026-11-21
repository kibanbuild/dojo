# Go 1.27.4 matches go.mod: the official golang image has this exact tag.
ARG GO_IMAGE=golang:1.27.4-alpine3.24@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280

FROM ${GO_IMAGE} AS build-api
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/dojo-api ./cmd/dojo-api
COPY internal/server ./internal/server
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/dojo-api ./cmd/dojo-api

FROM ${GO_IMAGE} AS build-goose
# Pinned migration tool, built here so it never enters go.mod. Keep in step with the Makefile.
ARG GOOSE_VERSION=v3.30.1
RUN CGO_ENABLED=0 GOBIN=/out go install -trimpath -ldflags='-s -w' \
    -tags='no_clickhouse no_mssql no_mysql no_sqlite3 no_vertica no_ydb no_turso' \
    github.com/pressly/goose/v3/cmd/goose@${GOOSE_VERSION}

FROM scratch AS api
COPY --from=build-api /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build-api /out/dojo-api /dojo-api
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/dojo-api"]

FROM scratch AS migrate
COPY --from=build-goose /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build-goose /out/goose /goose
COPY db/migrations /migrations
USER 65532:65532
ENV GOOSE_DRIVER=postgres GOOSE_MIGRATION_DIR=/migrations
ENTRYPOINT ["/goose"]
CMD ["up"]
