// SPDX-License-Identifier: AGPL-3.0-only
pluginManagement {
    repositories {
        google()
        mavenCentral()
        gradlePluginPortal()
    }
}

dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories {
        google()
        mavenCentral()
    }
}

rootProject.name = "ohs-android-example"

// The Kotlin SDK from this repository: Gradle substitutes
// org.openhealthsync:open-health-sync-kotlin with this build.
includeBuild("../../sdks/kotlin")

include(":app")
