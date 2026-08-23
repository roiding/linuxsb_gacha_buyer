# 构建阶段
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/gacha-buyer ./cmd/gacha-buyer

# 运行阶段
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/gacha-buyer /app/gacha-buyer
WORKDIR /app
VOLUME ["/app/data"]
EXPOSE 8080
ENTRYPOINT ["/app/gacha-buyer"]
CMD ["--data", "/app/data", "--listen", "0.0.0.0:8080"]
