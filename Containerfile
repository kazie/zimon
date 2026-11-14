# zimon in a container: the start page, kiwix-serve behind it, and the
# library in /data. See "Your own local internet server" in README.md.
#
#   podman build -t zimon .
#   podman run -d --name zimon -v zimon-data:/data -p 127.0.0.1:8080:8080 -p 127.0.0.1:8081:8081 zimon
#   podman exec zimon zimon add @linux
#
# zimon is cross-compiled on the build machine, and the final stage only
# copies files, so a multi-arch build runs nothing under emulation.

FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27.4-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# .git stays out of the build (.dockerignore), so the commit comes in as COMMIT.
ARG TARGETOS TARGETARCH VERSION=dev COMMIT=
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION -X main.commit=$COMMIT" -o /out/zimon .

# kiwix-serve, busybox and dumb-init on Alpine; it runs as "user" (uid 1001),
# who owns /data.
FROM ghcr.io/kiwix/kiwix-serve:3.8.2
COPY --from=build /out/zimon /usr/local/bin/zimon
COPY contrib/container/config.toml /etc/zimon/config.toml
COPY contrib/container/entrypoint.sh /usr/local/bin/zimon-entrypoint
ENV ZIMON_CONFIG=/data/config.toml
USER user
WORKDIR /data
VOLUME /data
EXPOSE 8080 8081
ENTRYPOINT ["/usr/bin/dumb-init", "--", "/usr/local/bin/zimon-entrypoint"]
CMD ["serve", "--address", "all", "--port", "8080"]
