# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS builder

WORKDIR /src

RUN apk add --no-cache ca-certificates git

COPY go.mod ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/redoproxy .

FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata wget

COPY --from=builder /out/redoproxy /usr/local/bin/redoproxy

ENTRYPOINT ["redoproxy"]