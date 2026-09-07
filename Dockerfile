# Build the binary against the exact Go version CI pins.
FROM golang:1.25.0-alpine AS build

# tzdata is not in the base image; it is copied into the final stage so an
# operator can set TZ and have log timestamps mean what they say.
RUN apk add --no-cache tzdata

WORKDIR /src

# Dependencies first, so a source-only change does not re-download them.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO is off so the result is a static binary that runs on scratch.
# Trimming paths keeps the build reproducible.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/harvester ./cmd/harvester

# scratch has no filesystem to speak of, so the writable directory the
# bolt backend needs has to be built here and copied in with the right
# owner. A VOLUME alone would create it owned by root, which the non-root
# user below could not write to.
RUN mkdir -p /data

# scratch, not alpine: the service opens no shell, reads no local files
# beyond its configuration, and runs one static binary. There is nothing
# for a package manager to do and nothing for an attacker to find.
FROM scratch

# Root certificates, without which every HTTPS fetch fails.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
# Zone data, so a timestamp in a log means what it says.
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo

COPY --from=build /out/harvester /harvester
COPY --from=build --chown=65532:65532 /data /data

# The example configuration becomes the default, so `docker run` produces
# visible output with no credentials and no setup. Mount over /configs to
# supply your own; see docker-compose.yml.
COPY configs/sources.example.yaml /configs/sources.yaml
COPY configs/sinks.example.yaml /configs/sinks.yaml

ENV SOURCES_FILE=/configs/sources.yaml \
    SINKS_FILE=/configs/sinks.yaml \
    DEDUPE_PATH=/data/dedupe.db

# 65532 is the conventional non-root uid for distroless-style images.
# /data must be writable by it when DEDUPE_BACKEND=bolt.
USER 65532:65532

VOLUME ["/data"]

ENTRYPOINT ["/harvester"]
