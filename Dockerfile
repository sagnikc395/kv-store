# syntax=docker/dockerfile:1

FROM golang:1.26.3-alpine AS test

WORKDIR /app

COPY go.mod ./
RUN go mod download

COPY . .

RUN go test ./...

CMD ["go", "test", "./..."]
