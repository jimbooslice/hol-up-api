FROM golang:1.26-alpine AS build

WORKDIR /src
COPY . .

RUN go test ./...
RUN CGO_ENABLED=0 go build -o /out/api ./cmd/api

FROM alpine:3.22

RUN apk add --no-cache ca-certificates \
    && adduser -D -H -u 10001 app

COPY --from=build /out/api /api

USER app
EXPOSE 8080
ENTRYPOINT ["/api"]
