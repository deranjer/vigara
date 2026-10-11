package org.vigara.companion

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.ByteArrayInputStream
import java.io.IOException

class CompanionBoundaryTest {
    @Test fun serverMustUseTrustedHttpsUrlShape() {
        assertTrue(isHttpsServer("https://vigara.example"))
        assertTrue(isHttpsServer("https://vigara.example/vigara"))
        assertFalse(isHttpsServer("http://vigara.example"))
        assertFalse(isHttpsServer("https://user:secret@vigara.example"))
        assertFalse(isHttpsServer("https://vigara.example?token=secret"))
        assertFalse(isHttpsServer("not a URL"))
    }

    @Test fun selectedDisplayNameCannotCarryAPathOrControls() {
        assertEquals("photo.jpg", SelectedFiles.safeDisplayName("photo.jpg"))
        assertEquals("photo.jpg", SelectedFiles.safeDisplayName("folder/photo.jpg"))
        assertEquals("photo.jpg", SelectedFiles.safeDisplayName("folder\\photo.jpg"))
        assertNull(SelectedFiles.safeDisplayName("\u0000secret"))
        assertNull(SelectedFiles.safeDisplayName(" "))
        assertNull(SelectedFiles.safeDisplayName("x".repeat(201)))
    }

    @Test fun refreshedFileMetadataAllowsNormalGrowthAndShrinkage() {
        val selected = SelectedFile("id", "content://selected", "old.txt", 4, "Ready")
        assertEquals(8L, SelectedFiles.refreshMetadata(selected, "new.txt", 8)?.size)
        assertEquals("new.txt", SelectedFiles.refreshMetadata(selected, "new.txt", 8)?.name)
        assertEquals(2L, SelectedFiles.refreshMetadata(selected, "new.txt", 2)?.size)
        assertNull(SelectedFiles.refreshMetadata(selected, "new.txt", 0))
        assertNull(SelectedFiles.refreshMetadata(selected, "new.txt", SelectedFiles.MAX_FILE_BYTES + 1))
    }

    @Test fun digestRejectsBothGrowthAndShrinkageDuringRead() {
        val bytes = "changed".toByteArray()
        assertEquals(64, SelectedFiles.digestSized(ByteArrayInputStream(bytes), bytes.size.toLong()).length)
        for (expected in listOf(bytes.size - 1L, bytes.size + 1L)) {
            try {
                SelectedFiles.digestSized(ByteArrayInputStream(bytes), expected)
                throw AssertionError("accepted stream size mismatch")
            } catch (_: IOException) { }
        }
    }
}
