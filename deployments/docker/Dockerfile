# --- build stage ---
FROM golang:1.25-alpine AS build
WORKDIR /src
# Cache modules first.
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
# Static, stripped binary.
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/api ./cmd/api

# --- runtime stage ---
# distroless: no shell, minimal surface, runs as nonroot by default.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/api /api
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/api"]
