FROM golang:1.24 as builder
ENV GOPROXY https://goproxy.cn,direct
WORKDIR /build/
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-X 'main.Version=$(date +%Y%m%d_%H%M%S)'" -o agent

FROM alpine:3.17
WORKDIR /app
COPY --from=builder /build/agent /app
RUN chmod +x agent
ENTRYPOINT ["./agent"]