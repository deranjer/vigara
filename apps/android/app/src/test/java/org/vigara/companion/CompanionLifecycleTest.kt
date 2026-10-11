package org.vigara.companion

import android.content.Context
import android.content.Intent
import android.net.Uri
import androidx.work.ListenableWorker
import androidx.work.testing.TestWorkerBuilder
import javax.crypto.spec.SecretKeySpec
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import kotlinx.coroutines.runBlocking
import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35])
class CompanionLifecycleTest {
    private lateinit var context: Context
    private lateinit var files: SelectedFiles
    private val testCredentialKey = SecretKeySpec(ByteArray(32) { it.toByte() }, "AES")

    private fun prefs() = CompanionPrefs(context) { testCredentialKey }

    @Before
    fun setUp() {
        context = RuntimeEnvironment.getApplication()
        context.getSharedPreferences("companion", Context.MODE_PRIVATE).edit().clear().commit()
        context.deleteDatabase("selected-files.db")
        files = SelectedFiles(context)
    }

    @After
    fun tearDown() {
        files.close()
    }

    @Test
    fun enrollmentMetadataSurvivesInstanceRecreationAndAnOldTelemetryAcknowledgementCannotClearNewState() {
        val prefs = prefs()
        val oldGeneration = prefs.setEnrollment("https://old.example", "old-agent", "old-credential")
        val oldBody = "old telemetry body"
        assertTrue(prefs.storeTelemetry(oldGeneration, oldBody))

        // This snapshot represents a request already in flight when enrollment changes.
        val inFlight = prefs.enrollmentSnapshot()
        val newGeneration = prefs.setEnrollment("https://new.example", "new-agent", "new-credential")
        assertNotEquals(oldGeneration, newGeneration)
        assertTrue(prefs.storeTelemetry(newGeneration, "new telemetry body"))

        val afterInstanceRecreation = prefs()
        assertEquals("https://new.example", afterInstanceRecreation.server)
        assertEquals("new-agent", afterInstanceRecreation.agentId)
        assertEquals("new-credential", afterInstanceRecreation.credential)
        assertEquals(newGeneration, afterInstanceRecreation.generation)
        assertFalse(afterInstanceRecreation.storeTelemetry(inFlight.generation, oldBody))

        // The old worker's successful response arrives after the new body was persisted.
        afterInstanceRecreation.clearTelemetry(inFlight.generation, inFlight.pendingTelemetryBody)
        assertEquals("new telemetry body", prefs().pendingTelemetryBody)
    }

    @Test
    fun telemetryWorkerKeepsNewEnrollmentBodyWhenAnOldRequestCompletesLate() {
        val prefs = prefs()
        val oldGeneration = prefs.setEnrollment("https://old.example", "old-agent", "old-credential")
        val requestStarted = CountDownLatch(1)
        val responseReleased = CountDownLatch(1)
        val worker = TestWorkerBuilder.from(context, TelemetryWorker::class.java).build()
        worker.prefsFactory = { appContext -> CompanionPrefs(appContext) { testCredentialKey } }
        worker.telemetryRequest = { server, path, credential, body, contentType ->
            assertEquals("https://old.example", server)
            assertEquals("/api/agent/telemetry", path)
            assertEquals("old-credential", credential)
            assertEquals("application/json", contentType)
            assertTrue(String(body, Charsets.UTF_8).contains("old-agent"))
            requestStarted.countDown()
            assertTrue("test response was not released", responseReleased.await(5, TimeUnit.SECONDS))
            204
        }

        val executor = Executors.newSingleThreadExecutor()
        try {
            val result = executor.submit<ListenableWorker.Result> { runBlocking { worker.doWork() } }
            assertTrue("worker did not start its request", requestStarted.await(5, TimeUnit.SECONDS))
            assertEquals(oldGeneration, prefs.enrollmentSnapshot().generation)

            val newGeneration = prefs.setEnrollment("https://new.example", "new-agent", "new-credential")
            assertTrue(prefs.storeTelemetry(newGeneration, "current enrollment body"))
            responseReleased.countDown()

            assertEquals(ListenableWorker.Result.success(), result.get(5, TimeUnit.SECONDS))
            assertEquals("current enrollment body", prefs.pendingTelemetryBody)
        } finally {
            responseReleased.countDown()
            executor.shutdownNow()
        }
    }

    @Test
    fun selectedFilesSurviveHelperRecreationAndRejectStaleWorkerStateUpdates() {
        val prefs = prefs()
        val firstGeneration = prefs.setEnrollment("https://one.example", "agent-one", "credential-one")
        val originalUri = Uri.parse("content://documents/original")
        assertTrue(files.add(
            originalUri,
            Intent.FLAG_GRANT_READ_URI_PERMISSION,
            firstGeneration,
            takePermission = { _, _ -> },
            releasePermission = { _, _ -> },
            metadata = { "before.txt" to 12L },
        ))
        val selectedId = files.all().single().id
        files.uploaded(selectedId, firstGeneration, "old-acknowledged-hash")
        assertEquals("old-acknowledged-hash", files.pending(firstGeneration).single().lastHash)
        files.pendingVersion(selectedId, firstGeneration, "old-hash", "old-backup-id")
        files.state(selectedId, firstGeneration, "Uploading selected file")

        val secondGeneration = prefs.setEnrollment("https://two.example", "agent-two", "credential-two")
        files.switchGeneration(secondGeneration)

        // A selected document can report new metadata on a later backup pass.
        val storedSelection = files.all().single()
        val changedMetadata = SelectedFiles.refreshMetadata(storedSelection, "after.txt", 30L)
        assertEquals("after.txt", changedMetadata?.name)
        assertEquals(30L, changedMetadata?.size)

        // A newly selected document is enrolled directly into the active generation.
        val newlySelectedUri = Uri.parse("content://documents/selected-after-enrollment")
        assertTrue(files.add(
            newlySelectedUri,
            Intent.FLAG_GRANT_READ_URI_PERMISSION,
            secondGeneration,
            takePermission = { _, _ -> },
            releasePermission = { _, _ -> },
            metadata = { "newly-selected.txt" to 9L },
        ))

        // Simulate a late version reservation, upload response, and status callback from the old worker.
        files.pendingVersion(selectedId, firstGeneration, "stale-hash", "stale-backup-id")
        files.uploaded(selectedId, firstGeneration, "old-hash")
        files.state(selectedId, firstGeneration, "Stale failure from old enrollment")

        files.close()
        files = SelectedFiles(context)
        val currentSelections = files.pending(secondGeneration)
            assertEquals(2, currentSelections.size)
            val switched = currentSelections.single { it.selected.id == selectedId }
            assertEquals("Ready", switched.selected.state)
            assertEquals("", switched.lastHash)
            assertEquals("", switched.pendingHash)
            assertEquals("", switched.pendingBackupId)

            val newlySelected = currentSelections.single { it.selected.uri == newlySelectedUri.toString() }
            assertEquals("newly-selected.txt", newlySelected.selected.name)
            assertEquals(9L, newlySelected.selected.size)
            assertEquals(secondGeneration, newlySelected.generation)
    }


    @Test
    fun selectedFileAdmissionKeepsReadOnlyOptInAndReleasesInvalidDocuments() {
        val generation = prefs().ensureGeneration()
        val uri = Uri.parse("content://documents/invalid")
        var tookPermission = false
        var releasedPermission = false

        assertFalse(files.add(
            uri,
            0,
            generation,
            takePermission = { _, _ -> tookPermission = true },
            releasePermission = { _, _ -> releasedPermission = true },
            metadata = { "ignored.txt" to 1L },
        ))
        assertFalse(tookPermission)

        assertFalse(files.add(
            uri,
            Intent.FLAG_GRANT_READ_URI_PERMISSION,
            generation,
            takePermission = { _, _ -> tookPermission = true },
            releasePermission = { _, _ -> releasedPermission = true },
            metadata = { "unsafe/\u0000name" to 1L },
        ))
        assertTrue(tookPermission)
        assertTrue(releasedPermission)
        assertTrue(files.all().isEmpty())
    }
}
