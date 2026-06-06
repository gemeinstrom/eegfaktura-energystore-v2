# syntax=docker/dockerfile:1
FROM golang:1.26 AS builder

WORKDIR /usr/src/app

COPY go.mod go.sum* ./
RUN go mod download && go mod verify

COPY . .

ENV CGO_ENABLED=0
RUN go build -ldflags="-s -w" -o /out/energystore-v2 ./cmd/server

FROM gcr.io/distroless/static:nonroot

ENV TZ="Europe/Vienna"

COPY --from=builder /out/energystore-v2 /usr/local/bin/energystore-v2

LABEL org.opencontainers.image.title="energystore-v2"
LABEL org.opencontainers.image.description="EEG Faktura Energiedaten-Store v2 (TimescaleDB, stateless, multi-replica)"
LABEL org.opencontainers.image.vendor="Verein zur Förderung von Erneuerbaren Energiegemeinschaften"
LABEL org.opencontainers.image.url=https://github.com/gemeinstrom/eegfaktura-energystore-v2
LABEL org.opencontainers.image.source=https://github.com/gemeinstrom/eegfaktura-energystore-v2
LABEL org.opencontainers.image.licenses=AGPL-3.0

EXPOSE 8080

USER nonroot

ENTRYPOINT ["/usr/local/bin/energystore-v2"]
