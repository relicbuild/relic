# The relic CLI comes from the server image the stack runs (compose builds it
# as relic-relic-server before this image).
ARG RELIC_IMAGE=relic-relic-server:latest
FROM ${RELIC_IMAGE} AS relic

FROM debian:trixie-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl git jq zip unzip nodejs npm openjdk-21-jdk-headless maven docker-cli golang-go python3 python3-venv python3-pip twine \
 && rm -rf /var/lib/apt/lists/*

ARG JF_VERSION=2.124.0
ARG JF_SHA256=5d9040dea5759b776f6961717d6f8633e29af19fb4f7f6e14ae2f1b6fddf6cf2
RUN curl -fsSL -o /usr/local/bin/jf "https://releases.jfrog.io/artifactory/jfrog-cli/v2-jf/${JF_VERSION}/jfrog-cli-linux-amd64/jf" \
 && echo "${JF_SHA256}  /usr/local/bin/jf" | sha256sum -c - \
 && chmod 0755 /usr/local/bin/jf

ARG GRADLE_VERSION=8.14.3
ARG GRADLE_SHA256=bd71102213493060956ec229d946beee57158dbd89d0e62b91bca0fa2c5f3531
RUN curl -fsSL -o /tmp/gradle.zip "https://services.gradle.org/distributions/gradle-${GRADLE_VERSION}-bin.zip" \
 && echo "${GRADLE_SHA256}  /tmp/gradle.zip" | sha256sum -c - \
 && unzip -q /tmp/gradle.zip -d /opt && rm /tmp/gradle.zip \
 && ln -s "/opt/gradle-${GRADLE_VERSION}/bin/gradle" /usr/local/bin/gradle

ARG HELM_VERSION=3.18.4
ARG HELM_SHA256=f8180838c23d7c7d797b208861fecb591d9ce1690d8704ed1e4cb8e2add966c1
RUN curl -fsSL -o /tmp/helm.tgz "https://get.helm.sh/helm-v${HELM_VERSION}-linux-amd64.tar.gz" \
 && echo "${HELM_SHA256}  /tmp/helm.tgz" | sha256sum -c - \
 && tar -xzf /tmp/helm.tgz -C /usr/local/bin --strip-components=1 linux-amd64/helm && rm /tmp/helm.tgz

ARG TERRAFORM_VERSION=1.13.3
ARG TERRAFORM_SHA256=71fc43d92ea09907be5d416d2405a6a9c2d1ceaed633f5e175c0af26e8c4b365
RUN curl -fsSL -o /tmp/tf.zip "https://releases.hashicorp.com/terraform/${TERRAFORM_VERSION}/terraform_${TERRAFORM_VERSION}_linux_amd64.zip" \
 && echo "${TERRAFORM_SHA256}  /tmp/tf.zip" | sha256sum -c - \
 && unzip -q /tmp/tf.zip terraform -d /usr/local/bin && rm /tmp/tf.zip

ARG DOTNET_VERSION=8.0.425
ARG DOTNET_SHA512=934b8060a7190e5909ad1fd0785db542f487b3bbf6cdd14826b02095fdd0d0394298b1634085eff302928fccc33f7c1a7253e9b87df555fc36fce819bcd2e798
RUN curl -fsSL -o /tmp/dotnet.tgz "https://builds.dotnet.microsoft.com/dotnet/Sdk/${DOTNET_VERSION}/dotnet-sdk-${DOTNET_VERSION}-linux-x64.tar.gz" \
 && echo "${DOTNET_SHA512}  /tmp/dotnet.tgz" | sha512sum -c - \
 && mkdir -p /usr/share/dotnet && tar -xzf /tmp/dotnet.tgz -C /usr/share/dotnet && rm /tmp/dotnet.tgz \
 && ln -s /usr/share/dotnet/dotnet /usr/local/bin/dotnet

RUN apt-get update && apt-get install -y --no-install-recommends ruby ruby-bundler && rm -rf /var/lib/apt/lists/*

RUN npm install -g @yarnpkg/cli-dist@4.9.2 pnpm@10.15.0 \
 && pip3 install --no-cache-dir --break-system-packages poetry==2.1.3 pipenv==2025.0.3

RUN python3 -m venv /opt/conan && /opt/conan/bin/pip install --no-cache-dir conan==2.10.2 \
 && ln -s /opt/conan/bin/conan /usr/local/bin/conan

RUN pip3 install --no-cache-dir --break-system-packages huggingface_hub==1.19.0

RUN apt-get update && apt-get install -y --no-install-recommends cargo && rm -rf /var/lib/apt/lists/*

ARG HELMFILE_VERSION=1.8.0
ARG HELMFILE_SHA256=35d5d39fc608342b23fc7ce1dd0c3bf0c96cc07ec3796507ab6894a5d172bf20
RUN curl -fsSL -o /tmp/helmfile.tgz "https://github.com/helmfile/helmfile/releases/download/v${HELMFILE_VERSION}/helmfile_${HELMFILE_VERSION}_linux_amd64.tar.gz" \
 && echo "${HELMFILE_SHA256}  /tmp/helmfile.tgz" | sha256sum -c - \
 && tar -xzf /tmp/helmfile.tgz -C /usr/local/bin helmfile && rm /tmp/helmfile.tgz

ARG CRANE_VERSION=0.22.1
ARG CRANE_SHA256=0ab7a1d6932a213aed964ce97666c3077fe691c8606413674a8b3e0b9ec4cda0
RUN curl -fsSL -o /tmp/crane.tgz "https://github.com/google/go-containerregistry/releases/download/v${CRANE_VERSION}/go-containerregistry_Linux_x86_64.tar.gz" \
 && echo "${CRANE_SHA256}  /tmp/crane.tgz" | sha256sum -c - \
 && tar -xzf /tmp/crane.tgz -C /usr/local/bin crane && rm /tmp/crane.tgz

ARG UV_VERSION=0.12.18
ARG UV_SHA256=89eadd7c76fc063887959510d5ba0ab1264dfd5f1143b925ddb73021a40acf16
RUN curl -fsSL -o /tmp/uv.tgz "https://github.com/astral-sh/uv/releases/download/${UV_VERSION}/uv-x86_64-unknown-linux-gnu.tar.gz" \
 && echo "${UV_SHA256}  /tmp/uv.tgz" | sha256sum -c - \
 && tar -xzf /tmp/uv.tgz -C /usr/local/bin --strip-components=1 uv-x86_64-unknown-linux-gnu/uv && rm /tmp/uv.tgz

ARG KIND_VERSION=0.33.0
ARG KIND_SHA256=aee6151561422756b764a4ae28e7f44cda5af5a9eead3cc9985112b1de8d8e0d
RUN curl -fsSL -o /usr/local/bin/kind "https://github.com/kubernetes-sigs/kind/releases/download/v${KIND_VERSION}/kind-linux-amd64" \
 && echo "${KIND_SHA256}  /usr/local/bin/kind" | sha256sum -c - \
 && chmod 0755 /usr/local/bin/kind

RUN apt-get update && apt-get install -y --no-install-recommends skopeo && rm -rf /var/lib/apt/lists/*

ARG TOFU_VERSION=1.12.6
ARG TOFU_SHA256=50a6106fa4de523d09c87af85f3db1dd47535fc005727fdca6852146476b88ec
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
 && mvn -B -q -Dmaven.repo.local=/opt/m2-seed -N org.apache.maven.plugins:maven-wrapper-plugin:3.3.2:wrapper -Dtype=only-script \
 && rm -rf /opt/m2-seed/relic /opt/m2-seed/org/apache/commons/commons-lang3/3.14.0 /tmp/maven-seed \
 && find /opt/m2-seed -name '_remote.repositories' -delete \
 && chmod -R a+rX /opt/m2-seed

ENV MAVEN_ARGS=-Dmaven.repo.local.tail=/opt/m2-seed MAVEN_OPTS=-Dmaven.repo.local.tail=/opt/m2-seed
RUN apt-get update && apt-get install -y --no-install-recommends rpm && rm -rf /var/lib/apt/lists/*
ENV JF=/usr/local/bin/jf CI=true JFROG_CLI_REPORT_USAGE=false \
    DOTNET_CLI_TELEMETRY_OPTOUT=1 DOTNET_NOLOGO=1 DOTNET_SKIP_FIRST_TIME_EXPERIENCE=1 \
    DOTNET_SYSTEM_GLOBALIZATION_INVARIANT=1
COPY api/adapters/artifactory/compat/e2e/run-in-container.sh /run.sh
COPY --from=relic /relic /usr/local/bin/relic
ENTRYPOINT ["/run.sh"]
