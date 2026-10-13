// SPDX-License-Identifier: AGPL-3.0-only
package org.openhealthsync.example

import android.content.Context
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.SemanticsNodeInteractionsProvider
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createEmptyComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTextReplacement
import androidx.test.core.app.ActivityScenario
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import org.junit.After
import org.junit.Assert.fail
import org.junit.Assume.assumeTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

/**
 * The whole app against a live server: first launch opens Settings, the test types the server
 * URL and an upload token, saves, enters a heart rate and uploads it through the UI.
 *
 * test/e2e/apps.sh runs it and then reads the sample back with the tenant key. It takes three
 * instrumentation arguments and is skipped without them:
 *   ohsServerUrl  the server as the emulator sees it, for example http://10.0.2.2:8080
 *   ohsToken      a user token with the upload scope
 *   ohsBpm        the heart rate to upload
 */
@RunWith(AndroidJUnit4::class)
class UploadFlowTest {
    @get:Rule
    val compose = createEmptyComposeRule()

    private lateinit var serverUrl: String
    private lateinit var token: String
    private lateinit var bpm: String
    private var scenario: ActivityScenario<MainActivity>? = null

    @Before
    fun setUp() {
        val args = InstrumentationRegistry.getArguments()
        serverUrl = args.getString("ohsServerUrl").orEmpty()
        token = args.getString("ohsToken").orEmpty()
        bpm = args.getString("ohsBpm").orEmpty()
        assumeTrue(
            "needs the ohsServerUrl, ohsToken and ohsBpm instrumentation arguments (test/e2e/apps.sh)",
            serverUrl.isNotEmpty() && token.isNotEmpty() && bpm.isNotEmpty(),
        )
        // A first launch: no saved URL or token.
        ApplicationProvider.getApplicationContext<Context>()
            .getSharedPreferences("ohs_example", Context.MODE_PRIVATE)
            .edit().clear().commit()
        scenario = ActivityScenario.launch(MainActivity::class.java)
    }

    @After
    fun tearDown() {
        scenario?.close()
    }

    @Test
    fun uploadsHeartRateThroughTheUi() {
        compose.onNodeWithText("Settings").assertExists()
        compose.field("Server URL").performTextReplacement(serverUrl)
        compose.field("User token").performTextReplacement(token)
        compose.onNodeWithText("Save").performClick()

        compose.field("Heart rate (bpm)").performTextReplacement(bpm)
        compose.onNodeWithText("Upload").performClick()

        compose.waitUntil(UPLOAD_TIMEOUT_MS) {
            compose.has("Uploaded") || compose.has("Upload failed")
        }
        if (compose.has("Upload failed")) {
            fail("upload failed: " + compose.texts().joinToString(" | "))
        }
        // One part, newly stored (a duplicate would say "duplicate (already stored)").
        compose.onNodeWithText("Part 1: HKQuantityTypeIdentifierHeartRate", substring = true)
            .assertExists()
        compose.onNodeWithText(", stored", substring = true).assertExists()
    }

    private fun SemanticsNodeInteractionsProvider.field(label: String) =
        onNode(hasSetTextAction() and hasText(label))

    private fun SemanticsNodeInteractionsProvider.has(text: String) =
        onAllNodes(hasText(text)).fetchSemanticsNodes().isNotEmpty()

    /** Every text on screen, for the failure message (the token field is masked). */
    private fun SemanticsNodeInteractionsProvider.texts(): List<String> =
        onAllNodes(hasText("", substring = true)).fetchSemanticsNodes()
            .flatMap { node -> node.config.getOrNull(SemanticsProperties.Text).orEmpty() }
            .map { it.text }

    private companion object {
        /** The SDK retries 5xx and network errors up to 5 times with backoff. */
        const val UPLOAD_TIMEOUT_MS = 60_000L
    }
}
