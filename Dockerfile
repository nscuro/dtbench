FROM alpine:3.23@sha256:5b10f432ef3da1b8d4c7eb6c487f2f5a8f096bc91145e68878dd4a5019afde11 as build
RUN apk add --no-cache ca-certificates

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY dtbench /
USER 1000
ENTRYPOINT ["/dtbench"]