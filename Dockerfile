# syntax=docker/dockerfile:1
# Build either binary: --build-arg TARGET=manager (default) or TARGET=shop.
FROM golang:1.24 AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
ARG TARGET=manager
ARG VERSION=dev
ARG COMMIT=none
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X github.com/Amitreddy14/tremor/internal/version.Version=${VERSION} -X github.com/Amitreddy14/tremor/internal/version.Commit=${COMMIT}" \
    -o /out/app ./cmd/${TARGET}

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/app /app
USER 65532:65532
EXPOSE 8080 8081
ENTRYPOINT ["/app"]
