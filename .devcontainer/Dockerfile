FROM mcr.microsoft.com/devcontainers/base:ubuntu-24.04

ENV WORKDIR="/home/vscode/add-controller"

WORKDIR ${WORKDIR}

COPY . .

ARG ARCH

ARG GO_VERSION
RUN export ARCH=${ARCH:-$(dpkg --print-architecture)} && \
    curl -fsSL "https://golang.org/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz" | tar -C /usr/local -xzf - && \
    echo 'export PATH=$PATH:/usr/local/go/bin:/home/vscode/go/bin' >> /home/vscode/.bashrc

ENV PATH="${PATH}:/usr/local/go/bin:/home/vscode/go/bin"
ENV GOPATH="/home/vscode/go"
ENV GOROOT="/usr/local/go"

RUN make tools
ENV PATH="${PATH}:/home/vscode/project-sveltos/hack/tools/bin"

ARG KUBECTX_VERSION
RUN export ARCH=${ARCH:-$(dpkg --print-architecture)} && \
    echo "Installing kubectx version ${KUBECTX_VERSION}..." && \
    curl -sLo /usr/local/bin/kubectx https://github.com/ahmetb/kubectx/releases/download/v${KUBECTX_VERSION}/kubectx && \
    chmod +x /usr/local/bin/kubectx

ARG SVELTOSCTL_VERSION
RUN export ARCH=${ARCH:-$(dpkg --print-architecture)} && \
    echo "Installing svletosctl version ${SVELTOSCTL_VERSION}..." && \
    curl -sLo /usr/local/bin/sveltosctl https://github.com/projectsveltos/sveltosctl/releases/download/v${SVELTOSCTL_VERSION}/sveltosctl-linux-${ARCH} && \
    chmod +x /usr/local/bin/sveltosctl
    