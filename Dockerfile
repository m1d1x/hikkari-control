FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN mkdir -p /out/data && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/downdetector ./cmd/downdetector

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/downdetector /downdetector
# The data volume is initialised from this directory, so it is writable by nonroot.
COPY --from=build --chown=nonroot:nonroot /out/data /data
USER nonroot
EXPOSE 8080
ENTRYPOINT ["/downdetector", "-config", "/etc/downdetector/config.yaml"]
