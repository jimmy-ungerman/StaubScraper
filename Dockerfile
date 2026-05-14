FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY main.go .
RUN CGO_ENABLED=0 go build -o staubscrape .

FROM alpine:3.22
RUN apk add --no-cache chromium
ENV CHROME_PATH=/usr/bin/chromium-browser
WORKDIR /app
COPY --from=builder /app/staubscrape .
EXPOSE 5123
CMD ["./staubscrape"]
