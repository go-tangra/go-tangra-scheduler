##################################
# Stage 0: Build frontend module
##################################

FROM node:20-alpine AS frontend-builder
RUN npm install -g pnpm@9
WORKDIR /frontend
COPY frontend/package.json frontend/pnpm-lock.yaml* ./
RUN pnpm install --frozen-lockfile || pnpm install
COPY frontend/ .
RUN pnpm build

##################################
# Stage 1: Build Go executable
##################################

FROM golang:1.25-alpine AS builder

ARG APP_VERSION=1.0.0

ENV GOTOOLCHAIN=auto

RUN apk add --no-cache git make curl

# Install buf for proto descriptor generation
RUN curl -sSL "https://github.com/bufbuild/buf/releases/latest/download/buf-$(uname -s)-$(uname -m)" -o /usr/local/bin/buf && \
    chmod +x /usr/local/bin/buf

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Install Wire for dependency injection code generation
RUN go install github.com/google/wire/cmd/wire@latest

# Regenerate proto descriptor
RUN buf build -o cmd/server/assets/descriptor.bin

# Generate Wire DI code
RUN cd cmd/server && wire

# Copy frontend dist into assets for go:embed
COPY --from=frontend-builder /frontend/dist cmd/server/assets/frontend-dist/

# Build the server
RUN CGO_ENABLED=0 \
    GOOS=linux \
    GOARCH=amd64 \
    go build -ldflags "-X main.version=${APP_VERSION} -s -w" \
    -o /src/bin/scheduler-server \
    ./cmd/server

##################################
# Stage 2: Create runtime image
##################################

FROM alpine:3.20

ARG APP_VERSION=1.0.0

RUN apk --no-cache add ca-certificates tzdata

ENV TZ=UTC
ENV GOLANG_PROTOBUF_REGISTRATION_CONFLICT=warn

WORKDIR /app

COPY --from=builder /src/bin/scheduler-server /app/bin/scheduler-server
COPY --from=builder /src/configs/ /app/configs/

RUN addgroup -g 1000 scheduler && \
    adduser -D -u 1000 -G scheduler scheduler && \
    chown -R scheduler:scheduler /app

USER scheduler:scheduler

# gRPC and HTTP ports
EXPOSE 10500 10501

CMD ["/app/bin/scheduler-server", "-c", "/app/configs"]

LABEL org.opencontainers.image.title="Scheduler Service" \
      org.opencontainers.image.description="Distributed task scheduling service with cron, delayed, and wait-result execution" \
      org.opencontainers.image.version="${APP_VERSION}"
