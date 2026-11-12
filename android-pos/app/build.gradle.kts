import java.util.Properties

// Android wrapper: the HCE service and the POS screen. AGP 9 compiles Kotlin itself.
plugins {
    alias(libs.plugins.android.application)
}

// POS settings live in the git-ignored local.properties (the API key must never be committed):
//   pos.apiBaseUrl=http://192.168.1.20:8080   pos.apiKey=…   pos.tagMode=metamask|plain
val localProps = Properties().apply {
    rootProject.file("local.properties").takeIf { it.exists() }?.reader()?.use { load(it) }
}
fun posSetting(key: String, default: String): String =
    (localProps.getProperty(key) ?: default).replace("\\", "\\\\").replace("\"", "\\\"")

android {
    namespace = "app.cryptopos.nfc"
    compileSdk = 37

    defaultConfig {
        applicationId = "app.cryptopos.nfc"
        minSdk = 26
        targetSdk = 37
        versionCode = 2
        versionName = "0.2"

        buildConfigField("String", "API_BASE_URL", "\"${posSetting("pos.apiBaseUrl", "http://10.0.2.2:8080")}\"")
        buildConfigField("String", "API_KEY", "\"${posSetting("pos.apiKey", "")}\"")
        buildConfigField("String", "TAG_MODE", "\"${posSetting("pos.tagMode", "metamask")}\"")
    }

    buildFeatures {
        buildConfig = true
    }

    testOptions {
        unitTests.isIncludeAndroidResources = true
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
}

dependencies {
    implementation(project(":tag"))

    testImplementation(testFixtures(project(":tag")))
    testImplementation(libs.junit4)
    testImplementation(libs.robolectric)
}
