package network.openvibe.link

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import network.openvibe.link.data.ActionItem
import network.openvibe.link.ui.ActivityScreen
import network.openvibe.link.ui.ApprovalsScreen
import network.openvibe.link.ui.LinkTheme
import network.openvibe.link.ui.PairScreen
import network.openvibe.link.ui.PhoneScreen
import org.json.JSONObject

/**
 * Debug builds only: draws one screen with sample data, so a screenshot job (or a designer) can see every screen
 * without an account. It is not in a release APK: it lives in src/debug.
 *
 *   adb shell am start -n network.openvibe.link/.PreviewActivity --es screen approvals
 */
class PreviewActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val screen = intent.getStringExtra("screen") ?: "approvals"
        setContent {
            LinkTheme {
                when (screen) {
                    "activity" -> ActivityScreen(preview = SAMPLE_ACTIVITY)
                    "phone" -> PhoneScreen(previewInfo = SAMPLE_DEVICE, preview = true)
                    "pair" -> PairScreen(initialCode = "ABCD-1234")
                    else -> ApprovalsScreen(preview = SAMPLE_APPROVALS)
                }
            }
        }
    }

    companion object {
        private fun item(
            id: String,
            title: String,
            summary: String?,
            bucket: String = "waiting",
            kind: String = "approval",
            agent: String? = "Actor",
            at: String = "2026-11-07T09:24:00Z",
        ) = ActionItem(
            service = "actor", id = id, kind = kind, title = title, summary = summary, bucket = bucket,
            state = if (bucket == "waiting") "pending" else "done", agent = agent,
            createdAt = at, updatedAt = at, actions = listOf("approve", "decline", "open"),
        )

        val SAMPLE_APPROVALS = listOf(
            item("apr_1", "Renew the domain for another year", "It would charge 14 USD to the card on file."),
            item("apr_2", "Use the password vault to sign in to the registrar", "The agent needs the saved password."),
            item("apr_3", "Delete the old nightly backups", "Twelve archives from last year would be removed."),
            item("apr_4", "Email the finished report to Dana", "Dana is not in your contacts yet."),
            item("apr_5", "Write a summary of yesterday's notes", null),
        )

        val SAMPLE_ACTIVITY = listOf(
            item("tsk_1", "Reviewing your pull request", "Step 3 of 7 · reading the diff", bucket = "running", kind = "agent_task", at = "2026-11-07T09:31:00Z"),
            item("tsk_2", "Monthly invoice for OpenVibe.Host", "Waiting for you: money", bucket = "waiting", kind = "agent_task", at = "2026-11-07T09:20:00Z"),
            item("tsk_3", "Nightly catch-up", "Runs at 03:00 on your computer", bucket = "scheduled", kind = "agent_task", at = "2026-11-07T03:00:00Z"),
            item("tsk_4", "Deploy the staging build", "Finished in 4m 12s", bucket = "done", kind = "agent_task", at = "2026-11-06T18:04:00Z"),
            item("tsk_5", "Index the photo library", "Stopped: the drive was full", bucket = "failed", kind = "agent_task", at = "2026-11-06T12:10:00Z"),
        )

        val SAMPLE_DEVICE = JSONObject()
            .put("device_id", "dev_01J9Z8K4Q7X2M5N8P3R6T0V")
            .put("principal", "nod_01J9Z8K4Q7X2M5N8P3R6T0V")
            .put("server", "https://openvibe.bot")
            .put("network", "https://openvibe.network")
            .put("kind", "phone")
            .put("bound", true)
    }
}
