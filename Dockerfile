FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY main.go .
RUN CGO_ENABLED=0 go build -o staubscrape .

FROM chromedp/headless-shell:stable
ENV CHROME_PATH=/headless-shell/headless-shell
WORKDIR /app
COPY --from=builder /app/staubscrape .
EXPOSE 5123
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
  CMD ["/app/staubscrape", "health"]
# The base image starts headless-shell and treats CMD as a browser argument.
ENTRYPOINT ["/app/staubscrape"]
CMD []
