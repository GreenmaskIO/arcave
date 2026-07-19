# Build the demo app as a static binary, then run it on a minimal alpine image.
# alpine ships busybox wget, which the compose healthcheck uses to hit /healthz.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /arcave-demo-app .

FROM alpine:3.20
COPY --from=build /arcave-demo-app /usr/local/bin/arcave-demo-app
EXPOSE 8080
ENTRYPOINT ["arcave-demo-app"]
