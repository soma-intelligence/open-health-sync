// SPDX-License-Identifier: AGPL-3.0-only
import org.jetbrains.kotlin.gradle.dsl.JvmTarget

plugins {
    id("com.android.library")
    id("org.jetbrains.kotlin.android")
}

// Placeholder coordinates, like the client's (docs/sdks/kotlin.md).
group = "org.openhealthsync"
version = "0.1.0"

repositories {
    google()
    mavenCentral()
}

android {
    namespace = "org.openhealthsync.sdk.healthconnect"
    // connect-client 1.1.0 asks for compileSdk 36 and the Android Gradle plugin 8.9.1.
    compileSdk = 36

    defaultConfig {
        // Health Connect itself needs API 26.
        minSdk = 26
        consumerProguardFiles("consumer-rules.pro")
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_11
        targetCompatibility = JavaVersion.VERSION_11
    }

    testOptions {
        unitTests {
            // The unit tests run on the JVM against fakes; nothing calls into android.jar.
            isReturnDefaultValues = true
            all { test ->
                // Scratch files stay under build/, and the tests find the server's own
                // fixture and mapper to compare with (skipped outside this repository).
                val tmp = layout.buildDirectory.dir("tmp/unit-tests").get().asFile
                test.systemProperty("java.io.tmpdir", tmp.path)
                test.systemProperty("ohs.repoRoot", rootProject.projectDir.resolve("../..").canonicalPath)
                test.doFirst { tmp.mkdirs() }
            }
        }
    }

    lint {
        abortOnError = true
        warningsAsErrors = false
    }
}

kotlin {
    compilerOptions {
        jvmTarget.set(JvmTarget.JVM_11)
        allWarningsAsErrors.set(true)
    }
}

dependencies {
    // The JVM client: OhsClient, the part builder and the upload route.
    api(project(":"))
    api("androidx.health.connect:connect-client:1.1.0")
    api("androidx.work:work-runtime:2.12.0")
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.9.0")

    testImplementation(kotlin("test-junit"))
    testImplementation("org.jetbrains.kotlinx:kotlinx-coroutines-test:1.9.0")
}
