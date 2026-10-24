FROM golang:1.26.4-bookworm AS backend
WORKDIR /src/server
COPY retrieval/ /src/retrieval/
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -o /community ./cmd/community

FROM backend AS tests
RUN go test -c -o /community-tests .

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=backend /community /app/community
ENV CACHE_DIR=/tmp/re-community-search PORT=8080
EXPOSE 8080
ENTRYPOINT ["/app/community"]
