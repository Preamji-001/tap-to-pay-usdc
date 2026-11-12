#!/usr/bin/env bash
# The pipeline gate. CI runs exactly this; run it locally before pushing.
# Green = every unit test and every simulated tap passed, lint is clean of errors, and the APK builds.
set -euo pipefail
cd "$(dirname "$0")/.."

studio_jdk="/Applications/Android Studio.app/Contents/jbr/Contents/Home"
if [ -z "${JAVA_HOME:-}" ] && [ -d "$studio_jdk" ]; then
  export JAVA_HOME="$studio_jdk"
fi

# check = :tag:test (protocol + simulated taps) and :app unit tests (Robolectric simulated taps
# against the real PosHceService on Android 11) + lint. assembleDebug proves the APK still builds.
./gradlew check :app:assembleDebug --console=plain

echo
echo "PIPELINE PASSED: protocol tests, tap simulations, lint and build are all green."
