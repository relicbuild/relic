#!/usr/bin/env bash
# covers: gradle, gradle-config
source "$(dirname "$0")/lib.sh"
if [[ -z "${SCENARIO_URL:-}" || -z "${SCENARIO_USER:-}" || -z "${SCENARIO_PASSWORD:-}" ]]; then
  echo "skipped: SCENARIO_URL/SCENARIO_USER/SCENARIO_PASSWORD not set" >&2
  exit 0
fi
REL=relic-scenario-gradle-release
CENTRAL=relic-scenario-gradle-central
VIRTUAL=relic-scenario-gradle
EXTRACTORS=relic-scenario-gradle-extractors
ALL=("$VIRTUAL" "$CENTRAL" "$REL" "$EXTRACTORS")
for r in "${ALL[@]}"; do delete_repo "$r"; done
cat >repos.json <<JSON
[{"key":"$REL","rclass":"local","packageType":"maven"},
 {"key":"$CENTRAL","rclass":"remote","packageType":"maven","url":"https://repo1.maven.org/maven2"},
 {"key":"$VIRTUAL","rclass":"virtual","packageType":"maven","repositories":"$REL,$CENTRAL","defaultDeploymentRepo":"$REL"},
 {"key":"$EXTRACTORS","rclass":"remote","packageType":"generic","url":"https://releases.jfrog.io"}]
JSON
jf rt rc repos.json
export JFROG_CLI_RELEASES_REPO="relic/$EXTRACTORS"

step "jf gradle artifactoryPublish with build info"
mkdir -p "$WORK/jfgradle/src/main/java/relic"
printf 'package relic;\npublic class G { public static String hi() { return org.apache.commons.lang3.StringUtils.capitalize("g"); } }\n' >"$WORK/jfgradle/src/main/java/relic/G.java"
echo 'rootProject.name = "jfgradle"' >"$WORK/jfgradle/settings.gradle"
cat >"$WORK/jfgradle/build.gradle" <<'GRADLE'
plugins { id 'java'; id 'maven-publish' }
group = 'relic.scenario'
version = '4.0.0'
dependencies { implementation 'org.apache.commons:commons-lang3:3.14.0' }
publishing { publications { mavenJava(MavenPublication) { from components.java } } }
GRADLE
# The e2e stack serves plain HTTP, which Gradle refuses without an opt-in.
cat >"$WORK/insecure.gradle" <<'GRADLE'
allprojects { repositories.withType(MavenArtifactRepository).configureEach { it.allowInsecureProtocol = true } }
GRADLE
gradle_args=()
[[ "$SCENARIO_URL" == http://* ]] && gradle_args=(--init-script "$WORK/insecure.gradle")
(cd "$WORK/jfgradle" &&
  jf gradlec --server-id-resolve relic --repo-resolve "$VIRTUAL" --server-id-deploy relic --repo-deploy "$REL" \
    --deploy-maven-desc &&
  jf gradle clean artifactoryPublish -q --no-daemon "${gradle_args[@]}" --build-name "$BUILD-gradle" --build-number 1 &&
  jf rt bp "$BUILD-gradle" 1)
jf rt s "$REL/relic/scenario/jfgradle/4.0.0/*" | expect_count 3 # jar, pom, module
jf rt curl -s "/api/build/$BUILD-gradle/1" | jq -e '.buildInfo.modules[0].artifacts | length >= 2' >/dev/null
jf rt curl -s "/api/build/$BUILD-gradle/1" | jq -e '.buildInfo.modules[0].dependencies | length >= 1' >/dev/null
jf rt s "$CENTRAL/org/apache/commons/commons-lang3/3.14.0/*.jar" | expect_count 1

for r in "${ALL[@]}"; do delete_repo "$r"; done
