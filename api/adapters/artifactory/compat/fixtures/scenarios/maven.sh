#!/usr/bin/env bash
# covers: mvn, mvn-config
# tier: platform (the Maven metadata job runs on the separate worker)
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_URL:-}" || -z "${SCENARIO_USER:-}" || -z "${SCENARIO_PASSWORD:-}" ]]; then
  echo "skipped: SCENARIO_URL/SCENARIO_USER/SCENARIO_PASSWORD not set" >&2
  exit 0
fi
REL=relic-scenario-mvn-release
SNAP=relic-scenario-mvn-snapshot
CENTRAL=relic-scenario-mvn-central
VIRTUAL=relic-scenario-mvn
EXTRACTORS=relic-scenario-extractors
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

step "mvn deploy: a release and two SNAPSHOT builds"
project "$WORK/lib" lib 1.0.0
(cd "$WORK/lib" && "${MVN[@]}" deploy)
project "$WORK/snap" snap 2.0.0-SNAPSHOT
(cd "$WORK/snap" && "${MVN[@]}" deploy && "${MVN[@]}" deploy)
jf rt s "$REL/relic/scenario/lib/1.0.0/*.jar" | expect_count 1
curl -sf -u "$SCENARIO_USER:$SCENARIO_PASSWORD" "$art/$SNAP/relic/scenario/snap/2.0.0-SNAPSHOT/maven-metadata.xml" | grep -q "<buildNumber>2</buildNumber>"

step "a consumer resolves both, plus commons-lang3 from Central, through the virtual"
project "$WORK/app" app 0.1.0 "<dependency><groupId>relic.scenario</groupId><artifactId>lib</artifactId><version>1.0.0</version></dependency>
<dependency><groupId>relic.scenario</groupId><artifactId>snap</artifactId><version>2.0.0-SNAPSHOT</version></dependency>
<dependency><groupId>org.apache.commons</groupId><artifactId>commons-lang3</artifactId><version>3.14.0</version></dependency>"
rm -rf "$WORK/m2/repository/relic"
(cd "$WORK/app" && "${MVN[@]}" package)
ls "$WORK/m2/repository/relic/scenario/snap/2.0.0-SNAPSHOT/" | grep -q -- '-2.jar$'
jf rt s "$CENTRAL/org/apache/commons/commons-lang3/3.14.0/*.jar" | expect_count 1

step "jf mvn deploy with build info (extractor fetched through Relic)"
export JFROG_CLI_RELEASES_REPO="relic/$EXTRACTORS"
project "$WORK/jfmvn" jfmvn 3.0.0 "<dependency><groupId>org.apache.commons</groupId><artifactId>commons-lang3</artifactId><version>3.14.0</version></dependency>"
(cd "$WORK/jfmvn" &&
  jf mvnc --server-id-resolve relic --repo-resolve-releases "$VIRTUAL" --repo-resolve-snapshots "$VIRTUAL" \
    --server-id-deploy relic --repo-deploy-releases "$REL" --repo-deploy-snapshots "$SNAP" &&
  jf mvn deploy -B -q -s "$WORK/m2/settings.xml" --build-name "$BUILD-mvn" --build-number 1 &&
  jf rt bp "$BUILD-mvn" 1)
jf rt s "$REL/relic/scenario/jfmvn/3.0.0/*" | expect_count 2
curl -sf -u "$SCENARIO_USER:$SCENARIO_PASSWORD" "$art/$REL/relic/scenario/jfmvn/maven-metadata.xml" | grep -q "<release>3.0.0</release>"
jf rt curl -s "/api/build/$BUILD-mvn/1" | jq -e '.buildInfo.modules[0].artifacts | length == 2' >/dev/null
jf rt curl -s "/api/build/$BUILD-mvn/1" | jq -e '.buildInfo.modules[0].dependencies | length >= 1' >/dev/null
jf rt s --build "$BUILD-mvn/1" "$REL/" | expect_count 2
jf rt s "$EXTRACTORS/artifactory/oss-release-local/org/jfrog/buildinfo/build-info-extractor-maven3/*/*.jar" | expect_count 1

for r in "${ALL[@]}"; do delete_repo "$r"; done
