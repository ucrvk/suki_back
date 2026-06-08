FROM golang:1.25-alpine AS builder

RUN apk add --no-cache \
    libavif-dev \
    gcc \
    musl-dev

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -o piccompress .

FROM alpine:3.21

RUN apk add --no-cache \
    libavif \
    ca-certificates

WORKDIR /app
COPY --from=builder /app/piccompress .
RUN mkdir -p data/pic

EXPOSE 6988

ENTRYPOINT ["./piccompress"]
CMD ["-listen", ":6988"]
