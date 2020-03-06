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

FROM ubuntu:18.04

ARG GOOGLE_CLOUD_SDK_VERSION
ENV GOOGLE_CLOUD_SDK_VERSION=${GOOGLE_CLOUD_SDK_VERSION:-283.0.0}

ARG HELM_VERSION
ENV HELM_VERSION=${HELM_VERSION:-3.1.1}

ARG KUBECTL_VERSION
ENV KUBECTL_VERSION=${KUBECTL_VERSION:-1.16.0}

ENV PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/lib/google-cloud-sdk/bin

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        git \
        curl \
        ca-certificates \
        python3 \
        python3-pip

ENTRYPOINT ["helmboot"]

RUN groupadd -g 1000 bob && \
    useradd -u 1000 -g 1000 -d /home/bob -m -k /etc/skel -s /bin/bash bob

RUN curl -L https://dl.google.com/dl/cloudsdk/channels/rapid/downloads/google-cloud-sdk-${GOOGLE_CLOUD_SDK_VERSION}-linux-x86.tar.gz | tar xvz -C /usr/lib && \
    /usr/lib/google-cloud-sdk/install.sh --command-completion=true

RUN curl https://get.helm.sh/helm-v${HELM_VERSION}-linux-386.tar.gz | tar xvz --strip-components 1 -C /usr/local/bin

RUN curl -Lo /usr/local/bin/kubectl https://storage.googleapis.com/kubernetes-release/release/v${KUBECTL_VERSION}/bin/linux/amd64/kubectl && \
    chmod 755 /usr/local/bin/kubectl

COPY --from=build /go/src/github.com/jenkins-x-labs/helmboot/build/linux/helmboot /usr/bin/helmboot

USER bob
