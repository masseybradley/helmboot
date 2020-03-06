# syntax = docker/dockerfile:experimental
FROM golang:1.12.9-buster as build

ENV GOPATH=/go

RUN --mount=type=cache,target=/var/cache/apt/archives \
    apt-get update && \
    apt-get install -y \
        make \
        go-dep

WORKDIR /go/src/github.com/jenkins-x-labs/helmboot

COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    make linux

FROM centos:7

RUN yum install -y git

ENTRYPOINT ["helmboot"]

COPY --from=build /go/src/github.com/jenkins-x-labs/helmboot/build/linux/helmboot /usr/bin/helmboot
