# ---- Frontend build stage ----
FROM node:22-alpine AS frontend-builder

RUN corepack enable

WORKDIR /app/frontend

# Install dependencies first (layer caching)
COPY frontend/package.json frontend/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile

# Copy frontend source and build
COPY frontend/ .

# Production build — VITE_API_URL is empty so the app uses relative paths (same origin)
RUN VITE_API_URL="" pnpm run build

# ---- Backend build stage ----
FROM golang:1.24-alpine AS backend-builder

RUN apk add --no-cache git

WORKDIR /app

COPY backend/go.mod backend/go.sum ./
RUN go mod download

COPY backend/ .
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o hll-radar .

# ---- Runtime stage ----
FROM alpine:latest

RUN apk --no-cache add ca-certificates tzdata wget

# Create non-root user
RUN addgroup -g 1001 -S appgroup && \
    adduser -u 1001 -S appuser -G appgroup

WORKDIR /app

# Copy backend binary and migrations
COPY --from=backend-builder /app/hll-radar .
COPY --from=backend-builder /app/database/migrations/ ./database/migrations/

# Copy built frontend — Go backend serves from ./static
COPY --from=frontend-builder /app/frontend/dist ./static/

# Create logs directory and set ownership
RUN mkdir -p logs && \
    chown -R appuser:appgroup /app

# Switch to non-root user
USER appuser

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=10s --start-period=10s --retries=3 \
    CMD wget --quiet --tries=1 --spider http://localhost:8080/health || exit 1

CMD ["./hll-radar"]
