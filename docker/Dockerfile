# Build OSM admin + locality extracts, then pack mmap lookup files.
# Usage: docker build -t geolookup-build -f docker/Dockerfile .
#        docker run --rm -v /data:/data geolookup-build /data/planet-latest.osm.pbf /data/out
FROM golang:1.26.3-bookworm AS build
WORKDIR /src
COPY go.mod ./
COPY . ./
RUN CGO_ENABLED=0 go build -o /geolookup-build ./cmd/geolookup-build

FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends osmium-tool ca-certificates curl \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /geolookup-build /usr/local/bin/geolookup-build
COPY docker/extract.sh /usr/local/bin/geolookup-extract
COPY docker/planet.sh /usr/local/bin/geolookup-planet
RUN chmod +x /usr/local/bin/geolookup-extract /usr/local/bin/geolookup-planet
ENTRYPOINT ["geolookup-extract"]
