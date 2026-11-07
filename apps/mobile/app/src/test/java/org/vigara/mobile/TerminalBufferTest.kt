package org.vigara.mobile

import org.junit.Assert.assertEquals
import org.junit.Test
import org.vigara.mobile.ui.common.TerminalBuffer

class TerminalBufferTest {
    private fun term(vararg chunks: String): String {
        val t = TerminalBuffer(cols = 20)
        chunks.forEach { t.feed(it.toByteArray()) }
        return t.text()
    }

    @Test fun plainTextAndNewlines() = assertEquals("a\nb", term("a\r\nb"))

    @Test fun carriageReturnOverwrites() = assertEquals("doneing", term("working\rdone"))

    @Test fun progressBarRewritesTheLine() = assertEquals("100%", term("10%\r50%\r100%"))

    @Test fun backspaceEraseLikeReadline() = assertEquals("ab", term("abc\b\u001b[K"))

    @Test fun colourSequencesAreStripped() = assertEquals("ok", term("\u001b[32mok\u001b[0m"))

    @Test fun oscTitleIsIgnored() = assertEquals("x", term("\u001b]0;title\u0007x"))

    @Test fun wrapsAtColumns() = assertEquals("12345678901234567890\nX", term("12345678901234567890X"))

    @Test fun splitEscapeAcrossChunks() = assertEquals("hi", term("\u001b[3", "1mhi"))

    @Test fun splitUtf8AcrossChunks() {
        val t = TerminalBuffer()
        val bytes = "é".toByteArray()
        t.feed(byteArrayOf(bytes[0])); t.feed(byteArrayOf(bytes[1]))
        assertEquals("é", t.text())
    }

    @Test fun deleteCharsShiftsLeft() = assertEquals("ac", term("abc\u001b[2D\u001b[P"))
}
