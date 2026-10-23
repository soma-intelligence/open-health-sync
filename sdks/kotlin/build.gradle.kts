// SPDX-License-Identifier: AGPL-3.0-only
plugins {
    kotlin("jvm") version "2.0.21"
    kotlin("plugin.serialization") version "2.0.21"
    `java-library`
    `maven-publish`
    signing
}

// Placeholder group until the Maven Central namespace is chosen; the publish workflow
// (.github/workflows/sdk-publish.yml) refuses to upload while it is org.openhealthsync.
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

// Maven Central wants a sources jar and a javadoc jar next to the library jar.
java {
    withSourcesJar()
    withJavadocJar()
}

// `./gradlew publishMavenPublicationToStagingRepository` writes the signed artifacts to
// build/staging-repo; the publish workflow zips that folder and uploads it to the Central
// Portal. Signing happens only when ORG_GRADLE_PROJECT_signingKey is set.
publishing {
    publications {
        create<MavenPublication>("maven") {
            from(components["java"])
            pom {
                name.set("Open Health Sync Kotlin SDK")
                description.set(
                    "Kotlin client for Open Health Sync, the open source health data aggregator: " +
                        "Apple Health, Health Connect, Oura, WHOOP, Withings and FHIR R4 through one API, " +
                        "for Android and the JVM.",
                )
                url.set("https://github.com/soma-intelligence/open-health-sync")
                licenses {
                    license {
                        name.set("AGPL-3.0-only")
                        url.set("https://www.gnu.org/licenses/agpl-3.0.txt")
                        distribution.set("repo")
                    }
                }
                developers {
                    developer {
                        id.set("QuentinCar0n")
                        name.set("QuentinCar0n")
                        url.set("https://github.com/QuentinCar0n")
                    }
                }
                scm {
                    url.set("https://github.com/soma-intelligence/open-health-sync")
                    connection.set("scm:git:https://github.com/soma-intelligence/open-health-sync.git")
                    developerConnection.set("scm:git:ssh://git@github.com/soma-intelligence/open-health-sync.git")
                }
                issueManagement {
                    system.set("GitHub")
                    url.set("https://github.com/soma-intelligence/open-health-sync/issues")
                }
            }
        }
    }
    repositories {
        maven {
            name = "staging"
            url = uri(layout.buildDirectory.dir("staging-repo"))
        }
    }
}

signing {
    val signingKey = providers.gradleProperty("signingKey").orNull?.takeIf { it.isNotBlank() }
    if (signingKey != null) {
        useInMemoryPgpKeys(signingKey, providers.gradleProperty("signingPassword").orNull)
        sign(publishing.publications["maven"])
    }
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
