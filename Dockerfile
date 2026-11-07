# syntax=docker/dockerfile:1
FROM golang:1.24 AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=none
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X github.com/Amitreddy14/tremor/internal/version.Version=${VERSION} -X github.com/Amitreddy14/tremor/internal/version.Commit=${COMMIT}" \
    -o /out/manager ./cmd/manager

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/manager /manager
USER 65532:65532
EXPOSE 8081
ENTRYPOINT ["/manager"]
