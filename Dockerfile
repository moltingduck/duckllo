FROM golang:1.26-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/duckllo ./cmd/duckllo

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/duckllo /usr/local/bin/duckllo
USER nonroot:nonroot
EXPOSE 3000
ENTRYPOINT ["/usr/local/bin/duckllo"]
CMD ["serve"]
