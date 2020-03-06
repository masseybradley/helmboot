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

ARG GOOGLE_CLOUD_SDK_VERSION
ENV GOOGLE_CLOUD_SDK_VERSION=${GOOGLE_CLOUD_SDK_VERSION:-283.0.0}

ARG HELM_VERSION
ENV HELM_VERSION=${HELM_VERSION:-3.1.1}

ENV PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/lib/google-cloud-sdk/bin

RUN yum install -y \
        git \
        curl

ENTRYPOINT ["helmboot"]

RUN curl -L https://dl.google.com/dl/cloudsdk/channels/rapid/downloads/google-cloud-sdk-${GOOGLE_CLOUD_SDK_VERSION}-linux-x86.tar.gz | tar xvz -C /usr/lib && \
    /usr/lib/google-cloud-sdk/install.sh --command-completion=true

RUN curl https://get.helm.sh/helm-v${HELM_VERSION}-linux-386.tar.gz | tar xvz --strip-components 1 -C /usr/local/bin

COPY --from=build /go/src/github.com/jenkins-x-labs/helmboot/build/linux/helmboot /usr/bin/helmboot
