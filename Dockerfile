FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /skoda-exporter ./cmd/skoda-exporter

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /skoda-exporter /skoda-exporter
EXPOSE 9877
ENTRYPOINT ["/skoda-exporter"]
