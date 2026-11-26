# Build context is demo/ so the shared module in demo/internal is available.
FROM golang:1.27.4-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOWORK=off
COPY internal/go.mod internal/go.sum internal/
COPY services/payments/go.mod services/payments/go.sum services/payments/
RUN cd services/payments && go mod download
COPY internal internal
COPY services/payments services/payments
RUN cd services/payments && go build -trimpath -ldflags="-s -w" -o /out/payments .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/payments /payments
EXPOSE 8080
ENTRYPOINT ["/payments"]
