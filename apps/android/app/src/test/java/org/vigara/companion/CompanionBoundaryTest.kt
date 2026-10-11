package org.vigara.companion

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

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
}
