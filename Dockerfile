FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/go-ledger .

FROM alpine:3.20
RUN adduser -D -u 10001 app
USER app
COPY --from=build /out/go-ledger /go-ledger
EXPOSE 8080
ENTRYPOINT ["/go-ledger"]
