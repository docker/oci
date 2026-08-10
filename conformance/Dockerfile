FROM golang:1.25-alpine AS build

ARG DISTRIBUTION_SPEC_REF=967efdc079b91785ad18c77cc4f8991a47feefbf

RUN apk add --no-cache ca-certificates git
RUN mkdir /src \
    && cd /src \
    && git init \
    && git remote add origin https://github.com/opencontainers/distribution-spec.git \
    && git fetch --depth 1 origin "${DISTRIBUTION_SPEC_REF}" \
    && git checkout --detach FETCH_HEAD

WORKDIR /src/conformance
RUN CGO_ENABLED=0 go build -o /conformance .

FROM alpine:3.21

RUN apk add --no-cache ca-certificates
COPY --from=build /conformance /conformance
WORKDIR /work
ENTRYPOINT ["/conformance"]
