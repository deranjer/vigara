package org.vigara.mobile.ui.common

import java.nio.ByteBuffer
import java.nio.CharBuffer
import java.nio.charset.CodingErrorAction

/** One terminal cell: a character with a foreground colour index (-1 = default) and bold flag. */
class Cell(var ch: Char, var fg: Int, var bold: Boolean)

/**
 * A small line-oriented terminal emulator. It understands what shells and CLI tools emit for
 * prompts, line editing, colours and progress bars: carriage return, backspace, tab, erase-in-line,
 * cursor left/right, delete/insert characters and SGR colours. Full-screen programs (vim, htop)
 * are not supported; their cursor addressing is ignored.
 */
class TerminalBuffer(var cols: Int = 80, private val maxLines: Int = 3000) {
    val lines = ArrayList<ArrayList<Cell>>().also { it.add(ArrayList()) }
    var row = 0
        private set
    var col = 0
        private set

    private enum class Mode { NORMAL, ESC, CSI, OSC, OSC_ESC, CHARSET }

    private var mode = Mode.NORMAL
    private val params = StringBuilder()
    private var fg = -1
    private var bold = false
    private val decoder = Charsets.UTF_8.newDecoder().onMalformedInput(CodingErrorAction.REPLACE).onUnmappableCharacter(CodingErrorAction.REPLACE)
    private var pending = ByteArray(0)

    /** Feeds raw terminal output bytes (UTF-8) into the buffer. */
    @Synchronized
    fun feed(bytes: ByteArray) {
        val input = ByteBuffer.wrap(pending + bytes)
        val out = CharBuffer.allocate(input.remaining() + 1)
        decoder.decode(input, out, false)
        pending = ByteArray(input.remaining()).also { input.get(it) }
        out.flip()
        while (out.hasRemaining()) accept(out.get())
    }

    @Synchronized
    fun snapshot(): List<List<Cell>> = lines.map { line -> line.map { Cell(it.ch, it.fg, it.bold) } }

    @Synchronized
    fun text(): String = lines.joinToString("\n") { l -> String(CharArray(l.size) { l[it].ch }).trimEnd() }.trimEnd()

    @Synchronized
    fun clear() { lines.clear(); lines.add(ArrayList()); row = 0; col = 0 }

    private fun accept(c: Char) {
        when (mode) {
            Mode.NORMAL -> normal(c)
            Mode.ESC -> when (c) {
                '[' -> { mode = Mode.CSI; params.setLength(0) }
                ']' -> mode = Mode.OSC
                '(', ')' -> mode = Mode.CHARSET
                else -> mode = Mode.NORMAL
            }
            Mode.CHARSET -> mode = Mode.NORMAL
            Mode.OSC -> when (c) { '\u0007' -> mode = Mode.NORMAL; '\u001b' -> mode = Mode.OSC_ESC }
            Mode.OSC_ESC -> mode = Mode.NORMAL
            Mode.CSI -> if (c in '@'..'~') { csi(c); mode = Mode.NORMAL } else params.append(c)
        }
    }

    private fun normal(c: Char) {
        when (c) {
            '\u001b' -> mode = Mode.ESC
            '\n' -> newline()
            '\r' -> col = 0
            '\b' -> if (col > 0) col--
            '\t' -> { val target = (col / 8 + 1) * 8; while (col < target) put(' ') }
            '\u0007', '\u0000' -> {}
            else -> if (c >= ' ') put(c)
        }
    }

    private fun current() = lines[row]

    private fun put(c: Char) {
        if (col >= cols) { newline(); col = 0 }
        val line = current()
        while (line.size < col) line.add(Cell(' ', -1, false))
        if (col < line.size) { line[col].ch = c; line[col].fg = fg; line[col].bold = bold } else line.add(Cell(c, fg, bold))
        col++
    }

    private fun newline() {
        if (row == lines.lastIndex) {
            lines.add(ArrayList())
            if (lines.size > maxLines) lines.removeAt(0) else row++
        } else row++
    }

    private fun csi(final: Char) {
        val raw = params.toString()
        if (raw.startsWith("?") || raw.startsWith(">") || raw.startsWith("=")) return // private modes: ignore
        val args = if (raw.isEmpty()) emptyList() else raw.split(';').map { it.toIntOrNull() ?: 0 }
        val n = args.firstOrNull()?.takeIf { it > 0 } ?: 1
        val line = current()
        when (final) {
            'm' -> sgr(args.ifEmpty { listOf(0) })
            'K' -> when (args.firstOrNull() ?: 0) {
                0 -> while (line.size > col) line.removeAt(line.lastIndex)
                1 -> for (i in 0 until minOf(col + 1, line.size)) { line[i].ch = ' '; line[i].fg = -1 }
                else -> line.clear()
            }
            'C' -> col = minOf(col + n, cols - 1)
            'D' -> col = maxOf(0, col - n)
            'G' -> col = (n - 1).coerceIn(0, cols - 1)
            'A' -> row = maxOf(0, row - n)
            'B' -> row = minOf(lines.lastIndex, row + n)
            'P' -> repeat(n) { if (col < line.size) line.removeAt(col) }
            '@' -> { while (line.size < col) line.add(Cell(' ', -1, false)); repeat(n) { line.add(col, Cell(' ', -1, false)) } }
            'X' -> for (i in col until minOf(col + n, line.size)) { line[i].ch = ' ' }
            'J' -> if ((args.firstOrNull() ?: 0) >= 2) { lines.clear(); lines.add(ArrayList()); row = 0; col = 0 }
        }
    }

    private fun sgr(args: List<Int>) {
        var i = 0
        while (i < args.size) {
            when (val a = args[i]) {
                0 -> { fg = -1; bold = false }
                1 -> bold = true
                22 -> bold = false
                in 30..37 -> fg = a - 30
                39 -> fg = -1
                in 90..97 -> fg = a - 90 + 8
                38 -> { // 38;5;n (256 colours): approximate the first 16, ignore truecolor
                    if (args.getOrNull(i + 1) == 5) { fg = (args.getOrNull(i + 2) ?: -1).let { if (it in 0..15) it else -1 }; i += 2 }
                    else if (args.getOrNull(i + 1) == 2) i += 4
                }
            }
            i++
        }
    }
}
