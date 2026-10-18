#!/usr/bin/env bash
# covers: mvnw
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_URL:-}" || -z "${SCENARIO_USER:-}" || -z "${SCENARIO_PASSWORD:-}" ]]; then
  echo "skipped: SCENARIO_URL/SCENARIO_USER/SCENARIO_PASSWORD not set" >&2
  exit 0
fi
REL=relic-scenario-mvnw-release
SNAP=relic-scenario-mvnw-snapshot
CENTRAL=relic-scenario-mvnw-central
VIRTUAL=relic-scenario-mvnw
EXTRACTORS=relic-scenario-mvnw-extractors
ALL=("$VIRTUAL" "$CENTRAL" "$SNAP" "$REL" "$EXTRACTORS")
for r in "${ALL[@]}"; do delete_repo "$r"; done
cat >repos.json <<JSON
[{"key":"$REL","rclass":"local","packageType":"maven"},
 {"key":"$SNAP","rclass":"local","packageType":"maven"},
 {"key":"$CENTRAL","rclass":"remote","packageType":"maven","url":"https://repo1.maven.org/maven2"},
 {"key":"$VIRTUAL","rclass":"virtual","packageType":"maven","repositories":"$REL,$SNAP,$CENTRAL","defaultDeploymentRepo":"$REL"},
 {"key":"$EXTRACTORS","rclass":"remote","packageType":"generic","url":"https://releases.jfrog.io"}]
JSON
jf rt rc repos.json
export JFROG_CLI_RELEASES_REPO="relic/$EXTRACTORS"

art="$SCENARIO_URL/artifactory"
mkdir -p "$WORK/m2"
cat >"$WORK/m2/settings.xml" <<XML
<settings>
  <localRepository>$WORK/m2/repository</localRepository>
  <servers>
    <server><id>relic</id><username>$SCENARIO_USER</username><password>$SCENARIO_PASSWORD</password></server>
  </servers>
  <mirrors>
    <mirror><id>relic</id><mirrorOf>*</mirrorOf><url>$art/$VIRTUAL</url></mirror>
  </mirrors>
  <profiles>
    <profile>
      <id>relic</id>
      <repositories>
        <repository><id>snapshots</id><url>$art/$VIRTUAL</url>
          <releases><enabled>false</enabled></releases><snapshots><enabled>true</enabled></snapshots></repository>
      </repositories>
    </profile>
  </profiles>
  <activeProfiles><activeProfile>relic</activeProfile></activeProfiles>
</settings>
XML
MVN=(mvn -B -q -s "$WORK/m2/settings.xml")

project() { # project <dir> <artifactId> <version> [dependency-xml]
  mkdir -p "$1/src/main/java/relic"
  cat >"$1/pom.xml" <<POM
<project xmlns="http://maven.apache.org/POM/4.0.0">
  <modelVersion>4.0.0</modelVersion>
  <groupId>relic.scenario</groupId><artifactId>$2</artifactId><version>$3</version>
  <properties><maven.compiler.release>17</maven.compiler.release><project.build.sourceEncoding>UTF-8</project.build.sourceEncoding></properties>
  <dependencies>${4:-}</dependencies>
  <distributionManagement>
    <repository><id>relic</id><url>$art/$REL</url></repository>
    <snapshotRepository><id>relic</id><url>$art/$SNAP</url></snapshotRepository>
  </distributionManagement>
</project>
POM
  printf 'package relic;\npublic class %s { public static String hello() { return "%s"; } }\n' "Lib${2//-/}" "$2" >"$1/src/main/java/relic/Lib${2//-/}.java"
}

step "jf mvnw with build info (wrapper and Maven distribution through Relic)"
project "$WORK/jfmvnw" jfmvnw 5.0.0 "<dependency><groupId>org.apache.commons</groupId><artifactId>commons-lang3</artifactId><version>3.14.0</version></dependency>"
(cd "$WORK/jfmvnw" &&
  "${MVN[@]}" -N org.apache.maven.plugins:maven-wrapper-plugin:3.3.4:wrapper -Dtype=only-script -Dmaven=3.9.16 &&
  sed -i "s|^distributionUrl=.*|distributionUrl=$art/$VIRTUAL/org/apache/maven/apache-maven/3.9.16/apache-maven-3.9.16-bin.zip|" .mvn/wrapper/maven-wrapper.properties &&
  JFROG_RUN_NATIVE=true MVNW_USERNAME="$SCENARIO_USER" MVNW_PASSWORD="$SCENARIO_PASSWORD" MAVEN_USER_HOME="$WORK/mvnw-home" \
    jf mvnw deploy -B -q -s "$WORK/m2/settings.xml" --build-name "$BUILD-mvnw" --build-number 1 &&
  jf rt bp "$BUILD-mvnw" 1)
ls "$WORK"/mvnw-home/wrapper/dists/apache-maven-3.9.16*/*/bin/mvn >/dev/null
jf rt s "$CENTRAL/org/apache/maven/apache-maven/3.9.16/apache-maven-3.9.16-bin.zip" | expect_count 1
jf rt s "$REL/relic/scenario/jfmvnw/5.0.0/*" | expect_count 2
jf rt curl -s "/api/build/$BUILD-mvnw/1" | jq -e '.buildInfo.modules[0].artifacts | length == 2' >/dev/null

for r in "${ALL[@]}"; do delete_repo "$r"; done
