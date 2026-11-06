// Versions and their sources are recorded in docs/DECISIONS.md (Android section).
plugins {
    id("com.android.application") version "9.5.1" apply false
    id("com.google.gms.google-services") version "4.5.0" apply false
    // AGP 9 compiles Kotlin itself ("built-in Kotlin") and bundles KGP 2.2.10. Declaring the newer
    // Kotlin Gradle plugin here (never applied) puts it on the build classpath, so built-in Kotlin
    // runs Kotlin 2.4.21 instead (the upgrade path documented in the AGP 9.0 release notes).
    id("org.jetbrains.kotlin.android") version "2.4.21" apply false
}
