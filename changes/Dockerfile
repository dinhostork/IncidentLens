FROM golang:1.27.4-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOWORK=off
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/changes .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/changes /changes
EXPOSE 8090
ENTRYPOINT ["/changes"]
