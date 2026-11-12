import org.jetbrains.kotlin.gradle.dsl.JvmTarget

// Pure-Kotlin NFC protocol logic: no Android dependencies, so it is unit-tested on the JVM.
plugins {
    alias(libs.plugins.kotlin.jvm)
    `java-test-fixtures` // src/testFixtures: the fake reader + tap scenarios, shared with :app tests
}

java {
    sourceCompatibility = JavaVersion.VERSION_17
    targetCompatibility = JavaVersion.VERSION_17
}

kotlin {
    compilerOptions { jvmTarget.set(JvmTarget.JVM_17) }
}

dependencies {
    testImplementation(platform(libs.junit.bom))
    testImplementation(libs.junit.jupiter)
    testRuntimeOnly(libs.junit.platform.launcher)
}

tasks.test {
    useJUnitPlatform()
    testLogging {
        events("passed", "failed", "skipped")
        exceptionFormat = org.gradle.api.tasks.testing.logging.TestExceptionFormat.FULL
    }
}
