FROM golang:1.26.4-alpine3.23 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/jungle-api ./cmd/api

FROM alpine:3.23

RUN apk add --no-cache ca-certificates && addgroup -S jungle && adduser -S -G jungle jungle
COPY --from=build /out/jungle-api /usr/local/bin/jungle-api
USER jungle
EXPOSE 8090
ENTRYPOINT ["/usr/local/bin/jungle-api"]
