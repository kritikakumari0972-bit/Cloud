FROM node:26-alpine AS web-build
WORKDIR /src/web
COPY web/package.json ./package.json
RUN npm install
COPY web/ ./
RUN npm run build

FROM golang:1.27-alpine AS api-build
WORKDIR /src/backend
COPY backend/go.mod ./go.mod
RUN go mod tidy
COPY backend/ ./
RUN go mod tidy && go mod verify
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/novavps .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && addgroup -S novavps && adduser -S -G novavps novavps
WORKDIR /app
COPY --from=api-build /out/novavps /usr/local/bin/novavps
COPY --from=web-build /src/web/dist /app/web/dist
RUN mkdir -p /app/data && chown -R novavps:novavps /app
ENV NOVAVPS_ADDR=:8080 NOVAVPS_DATA_DIR=/app/data NOVAVPS_WEB_DIR=/app/web/dist
VOLUME ["/app/data"]
EXPOSE 8080
USER novavps
ENTRYPOINT ["/usr/local/bin/novavps"]
