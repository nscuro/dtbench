FROM alpine:3.22@sha256:8a1f59ffb675680d47db6337b49d22281a139e9d709335b492be023728e11715 as build
RUN apk add --no-cache ca-certificates

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY dtbench /
USER 1000
ENTRYPOINT ["/dtbench"]