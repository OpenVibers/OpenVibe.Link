import java.util.Properties

plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
    id("org.jetbrains.kotlin.plugin.compose")
    id("org.jetbrains.kotlin.plugin.serialization")
    // Screenshots of every screen from the sample data (src/test/.../ScreensSnapshotTest.kt).
    id("app.cash.paparazzi")
}

// The Go core, bound by gomobile (android/scripts/build-core.sh) into android/app/libs/core.aar. It is built, never
// committed; the build says so instead of failing with an unresolved symbol.
val coreAar = layout.projectDirectory.file("libs/core.aar")
if (!coreAar.asFile.exists()) {
    throw GradleException("android/app/libs/core.aar is missing: run android/scripts/build-core.sh first (it needs the Android NDK).")
}

// A release build is signed only when the runner has a key (repo secrets): GH Actions writes android/keystore.properties.
val signingProps = rootProject.file("keystore.properties").takeIf { it.exists() }
    ?.let { Properties().apply { it.inputStream().use(::load) } }

// The server a build talks to. A release build always carries https://openvibe.network; a debug build may be pointed
// elsewhere (`-Pnetwork.url=https://staging.example`) and then says so in a banner on every screen. A QR or a link that
// names another server is refused, never followed (data/Servers.kt, data/PairLinks.kt).
val officialServer = "https://openvibe.network"
val networkUrl = (project.findProperty("network.url") as String?)?.trimEnd('/') ?: officialServer

android {
    namespace = "network.openvibe.link"
    compileSdk = 35

    defaultConfig {
        applicationId = "network.openvibe.link"
        minSdk = 26
        targetSdk = 35
        versionCode = 1
        versionName = "0.1.0"
        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
    }

    signingConfigs {
        if (signingProps != null) {
            create("release") {
                storeFile = file(signingProps.getProperty("storeFile"))
                storePassword = signingProps.getProperty("storePassword")
                keyAlias = signingProps.getProperty("keyAlias")
                keyPassword = signingProps.getProperty("keyPassword")
            }
        }
    }

    buildTypes {
        debug {
            buildConfigField("String", "NETWORK_URL", "\"$networkUrl\"")
            buildConfigField("boolean", "DEBUG_SERVER", "${networkUrl != officialServer}")
        }
        release {
            buildConfigField("String", "NETWORK_URL", "\"$officialServer\"")
            buildConfigField("boolean", "DEBUG_SERVER", "false")
            isMinifyEnabled = false
            if (signingProps != null) signingConfig = signingConfigs.getByName("release")
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    kotlinOptions {
        jvmTarget = "17"
    }

    buildFeatures {
        buildConfig = true
        compose = true
    }

    packaging {
        resources.excludes += setOf("/META-INF/{AL2.0,LGPL2.1}", "/META-INF/DEPENDENCIES")
    }
}

dependencies {
    implementation(files("libs/core.aar"))

    implementation("androidx.core:core-ktx:1.13.1")
    implementation("androidx.appcompat:appcompat:1.7.0")
    implementation("androidx.activity:activity-compose:1.9.3")
    implementation("androidx.lifecycle:lifecycle-runtime-compose:2.8.7")
    implementation("androidx.lifecycle:lifecycle-viewmodel-compose:2.8.7")

    val composeBom = platform("androidx.compose:compose-bom:2024.10.01")
    implementation(composeBom)
    implementation("androidx.compose.ui:ui")
    implementation("androidx.compose.ui:ui-tooling-preview")
    implementation("androidx.compose.material3:material3")
    // The generated kit (OvKit.kt) names Icons.Filled.Error and Icons.Filled.ExpandMore, which are in the extended set.
    implementation("androidx.compose.material:material-icons-extended")
    debugImplementation("androidx.compose.ui:ui-tooling")

    implementation("androidx.biometric:biometric:1.1.0")
    implementation("androidx.browser:browser:1.8.0")

    implementation("com.squareup.okhttp3:okhttp:4.12.0")
    implementation("org.jetbrains.kotlinx:kotlinx-serialization-json:1.7.3")
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.9.0")

    // The QR the web shows: the same library Bot's link page builds the code with, in an embedded scanner.
    implementation("com.journeyapps:zxing-android-embedded:4.3.0")

    testImplementation("junit:junit:4.13.2")
    androidTestImplementation("androidx.test.ext:junit:1.2.1")
}
