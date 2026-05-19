FROM golang:alpine AS builder

WORKDIR /app

RUN apk add --no-cache gcc musl-dev

COPY . .

RUN go mod init app
RUN go mod tidy

RUN CGO_ENABLED=1 GOOS=linux go build -o al3sal_bot .

FROM alpine:latest

WORKDIR /app

RUN apk --no-cache add ca-certificates tzdata
RUN mkdir -p data

COPY --from=builder /app/al3sal_bot .

CMD ["./al3sal_bot"]
