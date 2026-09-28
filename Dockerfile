FROM golang:1.27.1-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /goif ./cmd/goif

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /goif /goif
COPY config/sepolia.json /etc/goif/config.json
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/goif"]
CMD ["run", "-config", "/etc/goif/config.json"]
