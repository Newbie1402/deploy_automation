FROM golang:1.27.3-alpine3.24 AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/deploy-auto .

FROM docker:29.8.0-cli-alpine3.24
RUN apk add --no-cache git ca-certificates
COPY --from=build /out/deploy-auto /usr/local/bin/deploy-auto
ENV WORKSPACE_ROOT=/var/lib/deploy-agent/workspaces \
    DATA_DIR=/var/lib/deploy-agent/data \
    API_ADDR=127.0.0.1:8080
VOLUME ["/var/lib/deploy-agent"]
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/deploy-auto"]
