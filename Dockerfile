FROM golang:1.26-alpine AS builder
# Release builds pass the version (e.g. --build-arg VERSION=1.2.0); "dev" turns off the release check.
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=${VERSION}" -o /copilotgateway .

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /copilotgateway /copilotgateway
ENV HOME=/root
ENV COPILOTGW_HOST=0.0.0.0
ENV COPILOTGW_PORT=7777
EXPOSE 7777
ENTRYPOINT ["/copilotgateway"]
