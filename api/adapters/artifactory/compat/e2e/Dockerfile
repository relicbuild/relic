# The relic CLI comes from the server image the stack runs (compose builds it
# as relic-relic-server before this image).
ARG RELIC_IMAGE=relic-relic-server:latest
FROM ${RELIC_IMAGE} AS relic

FROM debian:trixie-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl git jq zip unzip xz-utils openjdk-21-jdk-headless maven docker-cli golang-go python3 python3-venv python3-pip twine \
 && rm -rf /var/lib/apt/lists/*

ARG JF_VERSION=2.127.0
ARG JF_SHA256=54974599482eb223e7559692ec670160bfe03a8f0ac006d316d1efe6135166f8
RUN curl -fsSL -o /usr/local/bin/jf "https://releases.jfrog.io/artifactory/jfrog-cli/v2-jf/${JF_VERSION}/jfrog-cli-linux-amd64/jf" \
 && echo "${JF_SHA256}  /usr/local/bin/jf" | sha256sum -c - \
 && chmod 0755 /usr/local/bin/jf

ARG GRADLE_VERSION=9.8.1
ARG GRADLE_SHA256=dce76f55f8e251a3a1f130eb120f30b3d271de2b76c9b0729d316b5a1b6dc01f
RUN curl -fsSL -o /tmp/gradle.zip "https://services.gradle.org/distributions/gradle-${GRADLE_VERSION}-bin.zip" \
 && echo "${GRADLE_SHA256}  /tmp/gradle.zip" | sha256sum -c - \
 && unzip -q /tmp/gradle.zip -d /opt && rm /tmp/gradle.zip \
 && ln -s "/opt/gradle-${GRADLE_VERSION}/bin/gradle" /usr/local/bin/gradle

ARG HELM_VERSION=4.3.1
ARG HELM_SHA256=0403a88ae20913d11b10a2467b50087721fea7581736da6b9d3992f5357fe54b
RUN curl -fsSL -o /tmp/helm.tgz "https://get.helm.sh/helm-v${HELM_VERSION}-linux-amd64.tar.gz" \
 && echo "${HELM_SHA256}  /tmp/helm.tgz" | sha256sum -c - \
 && tar -xzf /tmp/helm.tgz -C /usr/local/bin --strip-components=1 linux-amd64/helm && rm /tmp/helm.tgz

ARG TERRAFORM_VERSION=1.16.6
ARG TERRAFORM_SHA256=38034b38d03096399a7a1264a14561a8029b2b496fd1f169452656e53968f1b7
RUN curl -fsSL -o /tmp/tf.zip "https://releases.hashicorp.com/terraform/${TERRAFORM_VERSION}/terraform_${TERRAFORM_VERSION}_linux_amd64.zip" \
 && echo "${TERRAFORM_SHA256}  /tmp/tf.zip" | sha256sum -c - \
 && unzip -q /tmp/tf.zip terraform -d /usr/local/bin && rm /tmp/tf.zip

ARG DOTNET_VERSION=10.0.402
ARG DOTNET_SHA512=1904a98dda2db34ba78ba3c4fb1b224455329af91c341d83547843c1839e112b04ae699ee92fa2449a8f62249196a938723078c0c60b9f65c88539d5687729c2
RUN curl -fsSL -o /tmp/dotnet.tgz "https://builds.dotnet.microsoft.com/dotnet/Sdk/${DOTNET_VERSION}/dotnet-sdk-${DOTNET_VERSION}-linux-x64.tar.gz" \
 && echo "${DOTNET_SHA512}  /tmp/dotnet.tgz" | sha512sum -c - \
 && mkdir -p /usr/share/dotnet && tar -xzf /tmp/dotnet.tgz -C /usr/share/dotnet && rm /tmp/dotnet.tgz \
 && ln -s /usr/share/dotnet/dotnet /usr/local/bin/dotnet

RUN apt-get update && apt-get install -y --no-install-recommends ruby ruby-bundler && rm -rf /var/lib/apt/lists/*

ARG TARGETARCH
ARG NODE_VERSION=24.22.0
ARG NODE_SHA256_X64=cc2f601cee82d9a5a233302fe307abc600727035fd7d2c3a3571755989e95008
ARG NODE_SHA256_ARM64=fef00bb4d36e1c50104a9775a10359beee98f162b4104dd12472c75065c6a143
RUN if [ "$TARGETARCH" = arm64 ]; then arch=arm64 sum=$NODE_SHA256_ARM64; else arch=x64 sum=$NODE_SHA256_X64; fi \
 && curl -fsSL -o /tmp/node.tar.xz "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-${arch}.tar.xz" \
 && echo "${sum}  /tmp/node.tar.xz" | sha256sum -c - \
 && tar -xJf /tmp/node.tar.xz -C /usr/local --strip-components=1 --exclude='*.md' --exclude=LICENSE && rm /tmp/node.tar.xz

RUN npm install -g @yarnpkg/cli-dist@4.18.1 pnpm@12.14.0 @modelcontextprotocol/inspector@2.11.0 \
 && pip3 install --no-cache-dir --break-system-packages --ignore-installed poetry==2.5.1 pipenv==2026.10.0

RUN python3 -m venv /opt/conan && /opt/conan/bin/pip install --no-cache-dir conan==2.33.0 \
 && ln -s /opt/conan/bin/conan /usr/local/bin/conan

RUN pip3 install --no-cache-dir --break-system-packages --ignore-installed huggingface_hub==1.19.0

RUN apt-get update && apt-get install -y --no-install-recommends cargo && rm -rf /var/lib/apt/lists/*

ARG HELMFILE_VERSION=1.8.2
ARG HELMFILE_SHA256=f179485c5bb9f8e20fbd8ea16d8807915988aba4d60131ee864fb3232452716e
RUN curl -fsSL -o /tmp/helmfile.tgz "https://github.com/helmfile/helmfile/releases/download/v${HELMFILE_VERSION}/helmfile_${HELMFILE_VERSION}_linux_amd64.tar.gz" \
 && echo "${HELMFILE_SHA256}  /tmp/helmfile.tgz" | sha256sum -c - \
 && tar -xzf /tmp/helmfile.tgz -C /usr/local/bin helmfile && rm /tmp/helmfile.tgz

ARG CRANE_VERSION=0.22.1
ARG CRANE_SHA256=0ab7a1d6932a213aed964ce97666c3077fe691c8606413674a8b3e0b9ec4cda0
RUN curl -fsSL -o /tmp/crane.tgz "https://github.com/google/go-containerregistry/releases/download/v${CRANE_VERSION}/go-containerregistry_Linux_x86_64.tar.gz" \
 && echo "${CRANE_SHA256}  /tmp/crane.tgz" | sha256sum -c - \
 && tar -xzf /tmp/crane.tgz -C /usr/local/bin crane && rm /tmp/crane.tgz

ARG UV_VERSION=0.13.1
ARG UV_SHA256=eadd9d27c134ab5ac1ffd4473b97a741407f3ea238288f03121fc4deddf90220
RUN curl -fsSL -o /tmp/uv.tgz "https://github.com/astral-sh/uv/releases/download/${UV_VERSION}/uv-x86_64-unknown-linux-gnu.tar.gz" \
 && echo "${UV_SHA256}  /tmp/uv.tgz" | sha256sum -c - \
 && tar -xzf /tmp/uv.tgz -C /usr/local/bin --strip-components=1 uv-x86_64-unknown-linux-gnu/uv && rm /tmp/uv.tgz

ARG KIND_VERSION=0.33.0
ARG KIND_SHA256=aee6151561422756b764a4ae28e7f44cda5af5a9eead3cc9985112b1de8d8e0d
RUN curl -fsSL -o /usr/local/bin/kind "https://github.com/kubernetes-sigs/kind/releases/download/v${KIND_VERSION}/kind-linux-amd64" \
 && echo "${KIND_SHA256}  /usr/local/bin/kind" | sha256sum -c - \
 && chmod 0755 /usr/local/bin/kind

RUN apt-get update && apt-get install -y --no-install-recommends skopeo && rm -rf /var/lib/apt/lists/*

ARG TOFU_VERSION=1.13.1
ARG TOFU_SHA256=378ada19d4bc70c43732004e8159be771b23b9a5afdf059e5f8a2b3fa2c70a69
RUN curl -fsSL -o /tmp/tofu.tgz "https://github.com/opentofu/opentofu/releases/download/v${TOFU_VERSION}/tofu_${TOFU_VERSION}_linux_amd64.tar.gz" \
 && echo "${TOFU_SHA256}  /tmp/tofu.tgz" | sha256sum -c - \
 && tar -xzf /tmp/tofu.tgz -C /usr/local/bin tofu && rm /tmp/tofu.tgz

# mvn reads the plugins a build uses from a read-only tail of its local
# repository, so scenarios fetch only what they test through Relic
# (commons-lang3 3.14.0, which they depend on, is left out).
COPY api/adapters/artifactory/compat/e2e/maven-seed/ /tmp/maven-seed/
RUN cd /tmp/maven-seed \
 && mvn -B -q -Dmaven.repo.local=/opt/m2-seed deploy -DaltDeploymentRepository=seed::file:///tmp/maven-seed/deployed \
 && mvn -B -q -Dmaven.repo.local=/opt/m2-seed dependency:resolve dependency:get -Dartifact=relic.seed:seed:1.0.0 \
      -DremoteRepositories=seed::default::file:///tmp/maven-seed/deployed \
 && mvn -B -q -Dmaven.repo.local=/opt/m2-seed -N org.apache.maven.plugins:maven-wrapper-plugin:3.3.4:wrapper -Dtype=only-script \
 && rm -rf /opt/m2-seed/relic /opt/m2-seed/org/apache/commons/commons-lang3/3.14.0 /tmp/maven-seed \
 && find /opt/m2-seed -name '_remote.repositories' -delete \
 && chmod -R a+rX /opt/m2-seed

ENV MAVEN_ARGS=-Dmaven.repo.local.tail=/opt/m2-seed MAVEN_OPTS=-Dmaven.repo.local.tail=/opt/m2-seed
RUN apt-get update && apt-get install -y --no-install-recommends rpm && rm -rf /var/lib/apt/lists/*
ENV JF=/usr/local/bin/jf CI=true JFROG_CLI_REPORT_USAGE=false NODE_USE_SYSTEM_CA=1 \
    DOTNET_CLI_TELEMETRY_OPTOUT=1 DOTNET_NOLOGO=1 DOTNET_SKIP_FIRST_TIME_EXPERIENCE=1 \
    DOTNET_SYSTEM_GLOBALIZATION_INVARIANT=1
COPY api/adapters/artifactory/compat/e2e/run-in-container.sh /run.sh
COPY --from=relic /relic /usr/local/bin/relic
ENTRYPOINT ["/run.sh"]
