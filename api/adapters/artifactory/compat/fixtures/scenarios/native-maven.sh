#!/usr/bin/env bash
# covers:
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_TLS_URL:-}" || -z "${SCENARIO_USER:-}" ]] || ! command -v mvn >/dev/null || ! command -v gradle >/dev/null; then
  echo "skipped: SCENARIO_TLS_URL/SCENARIO_USER, mvn or gradle missing" >&2
  exit 0
fi
REL=relic-scenario-nmvn-release
SNAP=relic-scenario-nmvn-snapshot
CENTRAL=relic-scenario-nmvn-central
VIRTUAL=relic-scenario-nmvn
ALL=("$VIRTUAL" "$REL" "$SNAP" "$CENTRAL")
for r in "${ALL[@]}"; do delete_repo "$r"; done
cat >repos.json <<JSON
[{"key":"$REL","rclass":"local","packageType":"maven","handleReleases":true,"handleSnapshots":false},
 {"key":"$SNAP","rclass":"local","packageType":"maven","handleReleases":false,"handleSnapshots":true},
 {"key":"$CENTRAL","rclass":"remote","packageType":"maven","url":"https://repo1.maven.org/maven2"},
 {"key":"$VIRTUAL","rclass":"virtual","packageType":"maven","repositories":"$REL,$SNAP,$CENTRAL","defaultDeploymentRepo":"$REL"}]
JSON
jf rt rc repos.json
trap 'for r in "${ALL[@]}"; do delete_repo "$r"; done; rm -rf "$WORK"' EXIT
art="$SCENARIO_TLS_URL/maven"
creds="$SCENARIO_USER:$SCENARIO_PASSWORD"
meta() { curl -sf -u "$creds" "$art/$1/relic/native/$2/maven-metadata.xml"; } # meta <repo> <artifact>[/<version>]

cat >"$WORK/settings.xml" <<XML
<settings>
  <localRepository>$WORK/m2</localRepository>
  <servers>
    <server><id>relic</id><username>\${env.SCENARIO_USER}</username><password>\${env.SCENARIO_PASSWORD}</password></server>
  </servers>
  <mirrors><mirror><id>relic</id><mirrorOf>*</mirrorOf><url>$art/$VIRTUAL</url></mirror></mirrors>
  <profiles><profile><id>relic</id>
    <repositories><repository><id>relic</id><url>$art/$VIRTUAL</url>
      <releases><enabled>true</enabled></releases><snapshots><enabled>true</enabled><updatePolicy>always</updatePolicy></snapshots></repository></repositories>
  </profile></profiles>
  <activeProfiles><activeProfile>relic</activeProfile></activeProfiles>
</settings>
XML
# -C: strict checksums, so a checksum Relic serves wrong fails the build.
MVN=(mvn -B -q -C -s "$WORK/settings.xml")
MVNV=(mvn -B -ntp -C -s "$WORK/settings.xml") # verbose: resolution failures explain themselves
project() { # project <dir> <artifactId> <version> [dependency-xml]
  mkdir -p "$1/src/main/java/relic/native_"
  cat >"$1/pom.xml" <<POM
<project xmlns="http://maven.apache.org/POM/4.0.0">
  <modelVersion>4.0.0</modelVersion>
  <groupId>relic.native</groupId><artifactId>$2</artifactId><version>$3</version>
  <properties><maven.compiler.release>17</maven.compiler.release><project.build.sourceEncoding>UTF-8</project.build.sourceEncoding></properties>
  <dependencies>${4:-}</dependencies>
  <distributionManagement>
    <repository><id>relic</id><url>$art/$REL</url></repository>
    <snapshotRepository><id>relic</id><url>$art/$SNAP</url></snapshotRepository>
  </distributionManagement>
</project>
POM
  printf 'package relic.native_;\npublic class Lib { public static String version() { return "%s"; } }\n' "$3" >"$1/src/main/java/relic/native_/Lib.java"
}

step "anonymous access is refused"
[[ "$(curl -s -o /dev/null -w '%{http_code}' "$art/$VIRTUAL/org/apache/commons/commons-lang3/3.14.0/commons-lang3-3.14.0.pom")" == 401 ]]

step "mvn deploy: two releases and two SNAPSHOT builds"
for v in 1.0.0 1.1.0; do project "$WORK/lib-$v" lib "$v" && (cd "$WORK/lib-$v" && "${MVN[@]}" deploy); done
project "$WORK/lib-snap" lib 2.0.0-SNAPSHOT
(cd "$WORK/lib-snap" && "${MVN[@]}" deploy && "${MVN[@]}" deploy)

step "maven-metadata.xml: releases, merged through the virtual, SNAPSHOT versions"
m=$(meta "$REL" lib)
grep -q "<release>1.1.0</release>" <<<"$m" && grep -q "<latest>1.1.0</latest>" <<<"$m"
grep -q "<version>1.0.0</version>" <<<"$m" && grep -q "<version>1.1.0</version>" <<<"$m"
grep -q "<lastUpdated>[0-9]\{14\}</lastUpdated>" <<<"$m"
v=$(meta "$VIRTUAL" lib)
grep -q "<version>1.1.0</version>" <<<"$v" && grep -q "<version>2.0.0-SNAPSHOT</version>" <<<"$v"
grep -q "<release>1.1.0</release>" <<<"$v"
s=$(meta "$SNAP" lib/2.0.0-SNAPSHOT)
grep -q "<buildNumber>2</buildNumber>" <<<"$s"
ts=$(sed -n 's:.*<timestamp>\(.*\)</timestamp>.*:\1:p' <<<"$s")
grep -q "<extension>jar</extension>" <<<"$s" && grep -q "<value>2.0.0-$ts-2</value>" <<<"$s"
curl -sf -u "$creds" -o /dev/null "$art/$SNAP/relic/native/lib/2.0.0-SNAPSHOT/lib-2.0.0-$ts-2.jar"
central=$(curl -sf -u "$creds" "$art/$VIRTUAL/org/apache/commons/commons-lang3/maven-metadata.xml")
grep -q "<version>3.14.0</version>" <<<"$central"

step "a version range and a SNAPSHOT resolve through the virtual"
# Maven orders 2.0.0-SNAPSHOT below 2.0, so [1.0,2.0) would pick the
# SNAPSHOT; [1.0,1.5] must resolve to the newest release, 1.1.0.
project "$WORK/app" app 0.1.0 "<dependency><groupId>relic.native</groupId><artifactId>lib</artifactId><version>[1.0,1.5]</version></dependency>
<dependency><groupId>org.apache.commons</groupId><artifactId>commons-lang3</artifactId><version>3.14.0</version></dependency>"
# The consumer has its own local repository: the deploys above installed
# the SNAPSHOT into $WORK/m2, which Maven could otherwise prefer.
consumer=(-Dmaven.repo.local="$WORK/m2-consumer")
(cd "$WORK/app" && "${MVNV[@]}" "${consumer[@]}" dependency:resolve -DoutputFile=deps.txt && grep -q "relic.native:lib:jar:1.1.0" deps.txt)
"${MVNV[@]}" "${consumer[@]}" dependency:get -Dartifact=relic.native:lib:2.0.0-SNAPSHOT
ls "$WORK/m2-consumer/relic/native/lib/2.0.0-SNAPSHOT/" | grep -q -- "-2.jar$"
jf rt s "$CENTRAL/org/apache/commons/commons-lang3/3.14.0/commons-lang3-3.14.0.jar" | expect_count 1

step "Gradle configured by an init script: dynamic version, Central, publish"
export GRADLE_USER_HOME="$WORK/gradle-home"
mkdir -p "$GRADLE_USER_HOME/init.d"
cat >"$GRADLE_USER_HOME/init.d/relic.gradle" <<GRADLE
def relic = { MavenArtifactRepository repo ->
    repo.url = uri("$art/$VIRTUAL")
    repo.credentials { username = System.getenv('SCENARIO_USER'); password = System.getenv('SCENARIO_PASSWORD') }
}
settingsEvaluated { settings ->
    settings.pluginManagement.repositories { maven { relic(it) } }
}
allprojects {
    repositories { maven { relic(it) } }
}
GRADLE
mkdir -p "$WORK/gapp/src/main/java/relic/gapp" && cd "$WORK/gapp"
printf 'package relic.gapp;\npublic class App { public static String v() { return relic.native_.Lib.version() + org.apache.commons.lang3.StringUtils.capitalize("x"); } }\n' >src/main/java/relic/gapp/App.java
echo 'rootProject.name = "gapp"' >settings.gradle
cat >build.gradle <<GRADLE
plugins { id 'java-library'; id 'maven-publish' }
group = 'relic.native'
version = '3.0.0'
dependencies {
    api 'relic.native:lib:1.+'
    implementation 'org.apache.commons:commons-lang3:3.14.0'
}
publishing {
    publications { mavenJava(MavenPublication) { from components.java } }
    repositories {
        maven {
            name = 'relic'
            url = uri("$art/$REL")
            credentials { username = System.getenv('SCENARIO_USER'); password = System.getenv('SCENARIO_PASSWORD') }
        }
    }
}
GRADLE
gradle -q --no-daemon dependencies --configuration compileClasspath | grep -q "relic.native:lib:1.+ -> 1.1.0"
gradle -q --no-daemon build publish
cd "$WORK"
jf rt s "$REL/relic/native/gapp/3.0.0/*" | jq -e '[.[].path | split("/") | last] | (index("gapp-3.0.0.jar") and index("gapp-3.0.0.pom") and index("gapp-3.0.0.module")) != null' >/dev/null
grep -q "<release>3.0.0</release>" <<<"$(meta "$REL" gapp)"
