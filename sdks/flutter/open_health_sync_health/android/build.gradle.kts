// SPDX-License-Identifier: AGPL-3.0-only
import org.jetbrains.kotlin.gradle.dsl.JvmTarget
import org.jetbrains.kotlin.gradle.tasks.KotlinCompile

// Built as a module of the Flutter app, which brings the Android Gradle plugin, the Kotlin
// plugin and the Flutter embedding. The plugin versions are the app's.
plugins {
    id("com.android.library")
}

// The Android Gradle plugin compiles Kotlin itself from version 9; before it, the Kotlin
// plugin does.
val agpMajor = com.android.Version.ANDROID_GRADLE_PLUGIN_VERSION.substringBefore('.').toInt()
if (agpMajor < 9) {
    apply(plugin = "org.jetbrains.kotlin.android")
}

group = "org.openhealthsync.flutter.health"
version = "0.1.0"

repositories {
    google()
    mavenCentral()
}

android {
    namespace = "org.openhealthsync.flutter.health"
    // The Health Connect client 1.1.0 asks for compileSdk 36.
    compileSdk = 36

    defaultConfig {
        // Health Connect itself needs API 26.
        minSdk = 26
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    sourceSets {
        getByName("main") {
            java.srcDirs("src/main/kotlin")
        }
    }
}

tasks.withType<KotlinCompile>().configureEach {
    compilerOptions.jvmTarget.set(JvmTarget.JVM_17)
}

dependencies {
    // The Kotlin SDK's Health Connect module: it reads the store, maps the records and
    // uploads. Not on a registry yet: the app resolves it from a checkout of the repository
    // with includeBuild (docs/sdks/flutter.md).
    implementation("org.openhealthsync:open-health-sync-health-connect:0.1.0")
    implementation("androidx.startup:startup-runtime:1.2.0")
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.9.0")
}
