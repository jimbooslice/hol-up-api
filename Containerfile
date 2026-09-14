FROM node:24-alpine AS web-build

WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build && test -s dist/index.html

FROM golang:1.26-alpine AS build

WORKDIR /src
COPY . .

RUN go test ./...
RUN CGO_ENABLED=0 go build -o /out/api ./cmd/api

FROM alpine:3.22

RUN apk add --no-cache ca-certificates \
    && adduser -D -H -u 10001 app

COPY --from=build /out/api /api
COPY --from=web-build /src/web/dist /web/dist
ENV WEB_DIR=/web/dist

USER app
EXPOSE 8080
ENTRYPOINT ["/api"]
