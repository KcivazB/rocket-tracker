# Rocket Tracker server (multi-user dashboard + agent API).
#   docker build -t rocket-tracker-server .
# See docs/SELF_HOSTING.md and docker-compose.example.yml.

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=""
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w ${VERSION:+-X main.Version=$VERSION}" \
      -o /out/rltracker-server ./cmd/rltracker-server \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/rltracker-server /rltracker-server
COPY --from=build --chown=nonroot:nonroot /out/data /data
ENV RT_LISTEN=:8080 \
    RT_DATA_DIR=/data
EXPOSE 8080
VOLUME ["/data"]
USER nonroot:nonroot
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
  CMD ["/rltracker-server", "healthcheck"]
ENTRYPOINT ["/rltracker-server"]
