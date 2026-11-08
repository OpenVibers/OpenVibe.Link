package network.openvibe.link

import androidx.activity.compose.LocalActivityResultRegistryOwner
import androidx.activity.result.ActivityResultRegistry
import androidx.activity.result.ActivityResultRegistryOwner
import androidx.activity.result.contract.ActivityResultContract
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.Modifier
import androidx.core.app.ActivityOptionsCompat
import app.cash.paparazzi.DeviceConfig
import app.cash.paparazzi.Paparazzi
import network.openvibe.link.ui.ActivityScreen
import network.openvibe.link.ui.ApprovalsScreen
import network.openvibe.link.ui.LinkTheme
import network.openvibe.link.ui.PairScreen
import network.openvibe.link.ui.PhoneScreen
import org.junit.Rule
import org.junit.Test

/**
 * Every screen, drawn from the debug PreviewActivity's sample data, in dark and in light (Pixel 5). The pictures land
 * in src/test/snapshots: `gradle :app:recordPaparazziDebug` writes them, `verifyPaparazziDebug` compares. docs/android.md
 * shows the dark ones. A designer changes a screen, records, and looks.
 */
class ScreensSnapshotTest {
    @get:Rule
    val paparazzi = Paparazzi(deviceConfig = DeviceConfig.PIXEL_5, theme = "android:Theme.Material.NoActionBar")

    /** The pair screen asks for a scanner result; under Paparazzi there is no activity, so a registry that never launches. */
    private val noResults = object : ActivityResultRegistryOwner {
        override val activityResultRegistry = object : ActivityResultRegistry() {
            override fun <I, O> onLaunch(requestCode: Int, contract: ActivityResultContract<I, O>, input: I, options: ActivityOptionsCompat?) = Unit
        }
    }

    private fun shot(dark: Boolean, content: @Composable () -> Unit) = paparazzi.snapshot {
        CompositionLocalProvider(LocalActivityResultRegistryOwner provides noResults) {
            LinkTheme(dark = dark) {
                Surface(Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.background) { content() }
            }
        }
    }

    @Test fun approvalsDark() = shot(true) { ApprovalsScreen(preview = PreviewActivity.SAMPLE_APPROVALS) }
    @Test fun approvalsLight() = shot(false) { ApprovalsScreen(preview = PreviewActivity.SAMPLE_APPROVALS) }
    @Test fun approvalsEmptyDark() = shot(true) { ApprovalsScreen(preview = emptyList()) }
    @Test fun cameraDark() = shot(true) { ApprovalsScreen(preview = PreviewActivity.SAMPLE_CAMERA) }
    @Test fun cameraLight() = shot(false) { ApprovalsScreen(preview = PreviewActivity.SAMPLE_CAMERA) }
    @Test fun locationDark() = shot(true) { ApprovalsScreen(preview = PreviewActivity.SAMPLE_LOCATION) }
    @Test fun locationLight() = shot(false) { ApprovalsScreen(preview = PreviewActivity.SAMPLE_LOCATION) }
    @Test fun notifyDark() = shot(true) { ActivityScreen(preview = PreviewActivity.SAMPLE_NOTIFY) }
    @Test fun notifyLight() = shot(false) { ActivityScreen(preview = PreviewActivity.SAMPLE_NOTIFY) }
    @Test fun activityDark() = shot(true) { ActivityScreen(preview = PreviewActivity.SAMPLE_ACTIVITY) }
    @Test fun activityLight() = shot(false) { ActivityScreen(preview = PreviewActivity.SAMPLE_ACTIVITY) }
    @Test fun phoneDark() = shot(true) { PhoneScreen(previewInfo = PreviewActivity.SAMPLE_DEVICE, preview = true) }
    @Test fun phoneLight() = shot(false) { PhoneScreen(previewInfo = PreviewActivity.SAMPLE_DEVICE, preview = true) }
    @Test fun pairDark() = shot(true) { PairScreen(initialCode = "ABCD-1234") }
    @Test fun pairLight() = shot(false) { PairScreen(initialCode = "ABCD-1234") }
}
