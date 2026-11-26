# Build context is demo/ so the shared module in demo/internal is available.
FROM golang:1.27.4-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOWORK=off
COPY internal/go.mod internal/go.sum internal/
COPY services/orders/go.mod services/orders/go.sum services/orders/
RUN cd services/orders && go mod download
COPY internal internal
COPY services/orders services/orders
RUN cd services/orders && go build -trimpath -ldflags="-s -w" -o /out/orders .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/orders /orders
EXPOSE 8080
ENTRYPOINT ["/orders"]
