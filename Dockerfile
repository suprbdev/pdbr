# Multi-stage build: static pdbr binary on distroless.
#
# pdbcore is consumed through a `replace ../pdbcore` directive until it is
# tagged, so the build context must carry it: `make docker-build`, `make dev`
# and CI run `go mod vendor` first and Go picks the vendor tree up
# automatically (COPY . . includes it). Once pdbcore is tagged, drop the
# vendor step and split the COPY into `go.mod go.sum` + `go mod download` +
# `COPY . .` for layer caching.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/pdbr ./cmd/pdbr

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/pdbr /pdbr
EXPOSE 8080
# Distroless has no shell; healthchecks are performed by the orchestrator
# against GET /healthz.
ENTRYPOINT ["/pdbr"]
CMD ["serve"]
