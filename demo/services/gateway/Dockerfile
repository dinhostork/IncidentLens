# Build context is demo/ so the shared module in demo/internal is available.
FROM golang:1.27.4-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOWORK=off
COPY internal/go.mod internal/go.sum internal/
COPY services/gateway/go.mod services/gateway/go.sum services/gateway/
RUN cd services/gateway && go mod download
COPY internal internal
COPY services/gateway services/gateway
RUN cd services/gateway && go build -trimpath -ldflags="-s -w" -o /out/gateway .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/gateway /gateway
EXPOSE 8080
ENTRYPOINT ["/gateway"]
