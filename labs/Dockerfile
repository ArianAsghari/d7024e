FROM golang:1.22-alpine AS build

WORKDIR /app
COPY go.mod ./
COPY main.go ./
COPY kademlia/ ./kademlia/
RUN CGO_ENABLED=0 go build -o /kadlab .

FROM alpine:latest
COPY --from=build /kadlab /kadlab
ENTRYPOINT ["/kadlab"]
