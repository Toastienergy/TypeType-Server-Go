# The server is one static Go binary: built without cgo, it runs on distroless with nothing else in the image.
FROM --platform=$BUILDPLATFORM golang:1.27.4-alpine3.24 AS builder
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG REVISION=unknown
ARG BUILD_TIME=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags "-s -w \
      -X typetype.dev/server/internal/api.Version=$VERSION \
      -X typetype.dev/server/internal/api.Revision=$REVISION \
      -X typetype.dev/server/internal/api.ShortRevision=$(echo "$REVISION" | cut -c1-7) \
      -X typetype.dev/server/internal/api.BuildTime=$BUILD_TIME" \
    -o /out/typetype-server ./cmd/typetype-server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/typetype-server /typetype-server
EXPOSE 8080
ENTRYPOINT ["/typetype-server"]
