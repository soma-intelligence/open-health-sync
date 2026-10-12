// SPDX-License-Identifier: MIT
plugins {
    kotlin("jvm") version "2.0.21"
    kotlin("plugin.serialization") version "2.0.21"
    `java-library`
}

// Placeholder coordinates: nothing is published yet (docs/sdks/kotlin.md).
group = "org.openhealthsync"
version = "0.1.0"

repositories {
    mavenCentral()
}

dependencies {
    api("org.jetbrains.kotlinx:kotlinx-coroutines-core:1.9.0")
    api("org.jetbrains.kotlinx:kotlinx-serialization-json:1.7.3")
    testImplementation(kotlin("test"))
    testImplementation("org.jetbrains.kotlinx:kotlinx-coroutines-test:1.9.0")
}

// Java 11 bytecode with the Java 8 API surface Android offers: java.time and
// HttpURLConnection, no java.net.http.
kotlin {
    compilerOptions {
        jvmTarget.set(org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_11)
        freeCompilerArgs.add("-Xjdk-release=11")
        allWarningsAsErrors.set(true)
    }
}
tasks.withType<JavaCompile>().configureEach {
    options.release.set(11)
}

tasks.test {
    useJUnitPlatform()
}

// The quickstart example: `./gradlew runExample` with OHS_URL and OHS_KEY set.
val example by sourceSets.creating {
    compileClasspath += sourceSets.main.get().output + configurations.runtimeClasspath.get()
    runtimeClasspath += output + compileClasspath
}
tasks.register<JavaExec>("runExample") {
    group = "application"
    description = "Runs the quickstart example against OHS_URL with the tenant key OHS_KEY."
    classpath = example.runtimeClasspath
    mainClass.set("QuickstartKt")
}
