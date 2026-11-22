// SPDX-License-Identifier: AGPL-3.0-only
pluginManagement {
    repositories {
        google()
        mavenCentral()
        gradlePluginPortal()
    }
}

rootProject.name = "open-health-sync-kotlin"

// The Android library that reads Health Connect (health-connect/) needs the Android SDK. It
// is part of the build when one is found (ANDROID_HOME, ANDROID_SDK_ROOT or sdk.dir in
// local.properties), so the JVM client still builds and tests on a machine without it.
// -Pohs.android=false leaves it out, -Pohs.android=true insists on it.
fun androidSdkDir(): String? {
    for (name in listOf("ANDROID_HOME", "ANDROID_SDK_ROOT")) {
        val dir = providers.environmentVariable(name).orNull
        if (!dir.isNullOrBlank()) return dir
    }
    val local = File(rootDir, "local.properties")
    if (!local.isFile) return null
    val props = java.util.Properties()
    local.inputStream().use(props::load)
    return props.getProperty("sdk.dir")?.takeIf { it.isNotBlank() }
}

val wantAndroid = providers.gradleProperty("ohs.android").orNull
if (wantAndroid == "true" || (wantAndroid != "false" && androidSdkDir() != null)) {
    include(":open-health-sync-health-connect")
    project(":open-health-sync-health-connect").projectDir = File(rootDir, "health-connect")
}
