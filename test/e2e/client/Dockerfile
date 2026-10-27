# The client the e2e scenarios run in: every package manager they drive.
# The relic CLI is not in it: the tests mount the one of the server image
# under test at /usr/local/bin/relic, so this image does not change with
# Relic and builds from cache.
FROM debian:trixie-slim
ARG TARGETARCH
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl git jq zip unzip xz-utils openjdk-21-jdk-headless maven golang-go python3 python3-venv python3-pip twine \
 && rm -rf /var/lib/apt/lists/*

# Docker's own CLI, matching the docker:29-dind daemons; Debian's docker-cli is 26 and cannot push one
# platform of an image the containerd image store holds as an index.
ARG DOCKER_CLI_VERSION=5:29.9.1-1~debian.13~trixie
ARG DOCKER_APT_KEY_FINGERPRINT=9DC858229FC7DD38854AE2D88D81803C0EBFCD88
RUN apt-get update && apt-get install -y --no-install-recommends gnupg \
 && install -m 0755 -d /etc/apt/keyrings \
 && curl -fsSL https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc \
 && gpg --show-keys --with-colons /etc/apt/keyrings/docker.asc | grep -q "^fpr:::::::::${DOCKER_APT_KEY_FINGERPRINT}:" \
 && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian trixie stable" \
    >/etc/apt/sources.list.d/docker.list \
 && apt-get update && apt-get install -y --no-install-recommends "docker-ce-cli=${DOCKER_CLI_VERSION}" \
 && rm -rf /var/lib/apt/lists/*

ARG JF_VERSION=2.127.0
ARG JF_SHA256_AMD64=54974599482eb223e7559692ec670160bfe03a8f0ac006d316d1efe6135166f8
ARG JF_SHA256_ARM64=d6cf1d946d32e038f883c34c7399a45f4c0604095d090acf4273f259c6a2ed7a
RUN if [ "$TARGETARCH" = arm64 ]; then arch=arm64 sum=$JF_SHA256_ARM64; else arch=amd64 sum=$JF_SHA256_AMD64; fi \
 && curl -fsSL -o /usr/local/bin/jf "https://releases.jfrog.io/artifactory/jfrog-cli/v2-jf/${JF_VERSION}/jfrog-cli-linux-${arch}/jf" \
 && echo "${sum}  /usr/local/bin/jf" | sha256sum -c - \
 && chmod 0755 /usr/local/bin/jf

ARG GRADLE_VERSION=9.8.1
ARG GRADLE_SHA256=dce76f55f8e251a3a1f130eb120f30b3d271de2b76c9b0729d316b5a1b6dc01f
RUN curl -fsSL -o /tmp/gradle.zip "https://services.gradle.org/distributions/gradle-${GRADLE_VERSION}-bin.zip" \
 && echo "${GRADLE_SHA256}  /tmp/gradle.zip" | sha256sum -c - \
 && unzip -q /tmp/gradle.zip -d /opt && rm /tmp/gradle.zip \
 && ln -s "/opt/gradle-${GRADLE_VERSION}/bin/gradle" /usr/local/bin/gradle

ARG HELM_VERSION=4.3.1
ARG HELM_SHA256_AMD64=0403a88ae20913d11b10a2467b50087721fea7581736da6b9d3992f5357fe54b
ARG HELM_SHA256_ARM64=8cf8f66ecbc252ac8f7f06cca68aba88508ddfde3a6a34feb1678ad1694b4f88
RUN if [ "$TARGETARCH" = arm64 ]; then arch=arm64 sum=$HELM_SHA256_ARM64; else arch=amd64 sum=$HELM_SHA256_AMD64; fi \
 && curl -fsSL -o /tmp/helm.tgz "https://get.helm.sh/helm-v${HELM_VERSION}-linux-${arch}.tar.gz" \
 && echo "${sum}  /tmp/helm.tgz" | sha256sum -c - \
 && tar -xzf /tmp/helm.tgz -C /usr/local/bin --strip-components=1 "linux-${arch}/helm" && rm /tmp/helm.tgz

ARG TERRAFORM_VERSION=1.16.6
ARG TERRAFORM_SHA256_AMD64=38034b38d03096399a7a1264a14561a8029b2b496fd1f169452656e53968f1b7
ARG TERRAFORM_SHA256_ARM64=a750b5a11038f165846b2658c40c88cd7070aa67a61bab431a14ba27928ea9ea
RUN if [ "$TARGETARCH" = arm64 ]; then arch=arm64 sum=$TERRAFORM_SHA256_ARM64; else arch=amd64 sum=$TERRAFORM_SHA256_AMD64; fi \
 && curl -fsSL -o /tmp/tf.zip "https://releases.hashicorp.com/terraform/${TERRAFORM_VERSION}/terraform_${TERRAFORM_VERSION}_linux_${arch}.zip" \
 && echo "${sum}  /tmp/tf.zip" | sha256sum -c - \
 && unzip -q /tmp/tf.zip terraform -d /usr/local/bin && rm /tmp/tf.zip

ARG DOTNET_VERSION=10.0.402
ARG DOTNET_SHA512_X64=1904a98dda2db34ba78ba3c4fb1b224455329af91c341d83547843c1839e112b04ae699ee92fa2449a8f62249196a938723078c0c60b9f65c88539d5687729c2
ARG DOTNET_SHA512_ARM64=db85ae85527251fbb1290720ac3bf324bb1d9eea74516ef291ed4bb1f8ff6ec63a17593367f49b456115e8b24ead6d0c2ece2801e501f36a6cd4444ed01a498f
RUN if [ "$TARGETARCH" = arm64 ]; then arch=arm64 sum=$DOTNET_SHA512_ARM64; else arch=x64 sum=$DOTNET_SHA512_X64; fi \
 && curl -fsSL -o /tmp/dotnet.tgz "https://builds.dotnet.microsoft.com/dotnet/Sdk/${DOTNET_VERSION}/dotnet-sdk-${DOTNET_VERSION}-linux-${arch}.tar.gz" \
 && echo "${sum}  /tmp/dotnet.tgz" | sha512sum -c - \
 && mkdir -p /usr/share/dotnet && tar -xzf /tmp/dotnet.tgz -C /usr/share/dotnet && rm /tmp/dotnet.tgz \
 && ln -s /usr/share/dotnet/dotnet /usr/local/bin/dotnet

RUN apt-get update && apt-get install -y --no-install-recommends ruby ruby-bundler && rm -rf /var/lib/apt/lists/*

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

RUN pip3 install --no-cache-dir --break-system-packages --ignore-installed huggingface_hub==2.2.0 hf_xet==1.7.0

RUN apt-get update && apt-get install -y --no-install-recommends cargo && rm -rf /var/lib/apt/lists/*

ARG HELMFILE_VERSION=1.8.2
ARG HELMFILE_SHA256_AMD64=f179485c5bb9f8e20fbd8ea16d8807915988aba4d60131ee864fb3232452716e
ARG HELMFILE_SHA256_ARM64=9163f0d7ad088d243d7305ba09c3465aac02dfebd658d46c62dbdaa11c224b16
RUN if [ "$TARGETARCH" = arm64 ]; then arch=arm64 sum=$HELMFILE_SHA256_ARM64; else arch=amd64 sum=$HELMFILE_SHA256_AMD64; fi \
 && curl -fsSL -o /tmp/helmfile.tgz "https://github.com/helmfile/helmfile/releases/download/v${HELMFILE_VERSION}/helmfile_${HELMFILE_VERSION}_linux_${arch}.tar.gz" \
 && echo "${sum}  /tmp/helmfile.tgz" | sha256sum -c - \
 && tar -xzf /tmp/helmfile.tgz -C /usr/local/bin helmfile && rm /tmp/helmfile.tgz

ARG CRANE_VERSION=0.22.1
ARG CRANE_SHA256_AMD64=0ab7a1d6932a213aed964ce97666c3077fe691c8606413674a8b3e0b9ec4cda0
ARG CRANE_SHA256_ARM64=898c0cff975f898a33e8c4580bdafb0e7c02c7faa33374e946762f97c4ab7110
RUN if [ "$TARGETARCH" = arm64 ]; then arch=arm64 sum=$CRANE_SHA256_ARM64; else arch=x86_64 sum=$CRANE_SHA256_AMD64; fi \
 && curl -fsSL -o /tmp/crane.tgz "https://github.com/google/go-containerregistry/releases/download/v${CRANE_VERSION}/go-containerregistry_Linux_${arch}.tar.gz" \
 && echo "${sum}  /tmp/crane.tgz" | sha256sum -c - \
 && tar -xzf /tmp/crane.tgz -C /usr/local/bin crane && rm /tmp/crane.tgz

ARG UV_VERSION=0.13.1
ARG UV_SHA256_AMD64=eadd9d27c134ab5ac1ffd4473b97a741407f3ea238288f03121fc4deddf90220
ARG UV_SHA256_ARM64=9041c73c99fdc5892cf128140beb354eca9231d502068cc36e6cb53a8e59ad14
RUN if [ "$TARGETARCH" = arm64 ]; then arch=aarch64 sum=$UV_SHA256_ARM64; else arch=x86_64 sum=$UV_SHA256_AMD64; fi \
 && curl -fsSL -o /tmp/uv.tgz "https://github.com/astral-sh/uv/releases/download/${UV_VERSION}/uv-${arch}-unknown-linux-gnu.tar.gz" \
 && echo "${sum}  /tmp/uv.tgz" | sha256sum -c - \
 && tar -xzf /tmp/uv.tgz -C /usr/local/bin --strip-components=1 "uv-${arch}-unknown-linux-gnu/uv" && rm /tmp/uv.tgz

ARG KIND_VERSION=0.33.0
ARG KIND_SHA256_AMD64=aee6151561422756b764a4ae28e7f44cda5af5a9eead3cc9985112b1de8d8e0d
ARG KIND_SHA256_ARM64=20022bee6cfcd5086cb7234d218e3454e6090022f2a8f55d1fa7fcf42c3867a2
RUN if [ "$TARGETARCH" = arm64 ]; then arch=arm64 sum=$KIND_SHA256_ARM64; else arch=amd64 sum=$KIND_SHA256_AMD64; fi \
 && curl -fsSL -o /usr/local/bin/kind "https://github.com/kubernetes-sigs/kind/releases/download/v${KIND_VERSION}/kind-linux-${arch}" \
 && echo "${sum}  /usr/local/bin/kind" | sha256sum -c - \
 && chmod 0755 /usr/local/bin/kind

RUN apt-get update && apt-get install -y --no-install-recommends skopeo && rm -rf /var/lib/apt/lists/*

ARG TOFU_VERSION=1.13.1
ARG TOFU_SHA256_AMD64=378ada19d4bc70c43732004e8159be771b23b9a5afdf059e5f8a2b3fa2c70a69
ARG TOFU_SHA256_ARM64=9c1ef375aa1852db0b2888aa921b640c71f8140d4682aa4fec99378a64fa7dc3
RUN if [ "$TARGETARCH" = arm64 ]; then arch=arm64 sum=$TOFU_SHA256_ARM64; else arch=amd64 sum=$TOFU_SHA256_AMD64; fi \
 && curl -fsSL -o /tmp/tofu.tgz "https://github.com/opentofu/opentofu/releases/download/v${TOFU_VERSION}/tofu_${TOFU_VERSION}_linux_${arch}.tar.gz" \
 && echo "${sum}  /tmp/tofu.tgz" | sha256sum -c - \
 && tar -xzf /tmp/tofu.tgz -C /usr/local/bin tofu && rm /tmp/tofu.tgz

# mvn reads the plugins a build uses from a read-only tail of its local
# repository, so scenarios fetch only what they test through Relic
# (commons-lang3 3.14.0, which they depend on, is left out).
COPY test/e2e/client/maven-seed/ /tmp/maven-seed/
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
RUN apt-get update && apt-get install -y --no-install-recommends git-lfs && rm -rf /var/lib/apt/lists/*
ENV JF=/usr/local/bin/jf CI=true JFROG_CLI_REPORT_USAGE=false NODE_USE_SYSTEM_CA=1 \
    DOTNET_CLI_TELEMETRY_OPTOUT=1 DOTNET_NOLOGO=1 DOTNET_SKIP_FIRST_TIME_EXPERIENCE=1 \
    DOTNET_SYSTEM_GLOBALIZATION_INVARIANT=1
COPY test/e2e/client/setup.sh test/e2e/client/scenario.sh /e2e/
CMD ["sleep", "infinity"]
