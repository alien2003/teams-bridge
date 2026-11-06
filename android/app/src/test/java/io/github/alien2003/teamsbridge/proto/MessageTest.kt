package io.github.alien2003.teamsbridge.proto

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertThrows
import org.junit.Test

/**
 * JSON parsing that matches Go's json.Unmarshal into proto.Message + Validate for the well-formed
 * documents the Go encoder emits (see Message.parse for where the parsers differ).
 */
class MessageTest {
    private val base = """"v":1,"t":"msg","id":"x","ts":1767225600"""

    @Test
    fun missingAndNullFieldsAreEmpty() {
        val m = Message.parse("""{$base}""")
        assertEquals(Message(1, "msg", "x", 1767225600), m)
        val n = Message.parse("""{$base,"title":null,"body":null,"url":null,"tag":null,"kind":null}""")
        assertEquals(m, n)
        assertEquals(0L, Message.parse("""{"v":1,"t":"hb","id":"x"}""").ts)
    }

    @Test
    fun unknownFieldsIgnoredAndOrderFree() {
        val m = Message.parse("""{"extra":{"a":[1,2]},"kind":"browser_dead","t":"alert","id":"y","ts":5,"v":1,"z":true}""")
        assertEquals(Message(1, "alert", "y", 5, kind = "browser_dead"), m)
    }

    @Test
    fun escapesAndUnicode() {
        val m = Message.parse("""{$base,"title":"Boрис \"q\" \\ \/","body":"line1\nline2 😀 <b>&amp;</b>"}""")
        assertEquals("Boрис \"q\" \\ /", m.title)
        assertEquals("line1\nline2 😀 <b>&amp;</b>", m.body)
    }

    @Test
    fun wrongTypesAreRejected() {
        val bad = listOf(
            """{"v":"1","t":"msg","id":"x","ts":1}""", // v must be a number
            """{"v":1.0,"t":"msg","id":"x","ts":1}""", // …an integer
            """{"v":1,"t":"msg","id":"x","ts":"1"}""",
            """{"v":1,"t":"msg","id":"x","ts":1.5}""",
            """{"v":1,"t":"msg","id":7,"ts":1}""",
            """{"v":1,"t":["msg"],"id":"x","ts":1}""",
            """{$base,"title":5}""",
            """{$base,"body":{}}""",
            """{$base,"url":false}""",
        )
        for (s in bad) assertThrows(s, ProtoException::class.java) { Message.parse(s) }
    }

    @Test
    fun validation() {
        assertThrows(ProtoException::class.java) { Message.parse("""{"v":2,"t":"msg","id":"x","ts":1}""") }
        assertThrows(ProtoException::class.java) { Message.parse("""{"t":"msg","id":"x","ts":1}""") }
        assertThrows(ProtoException::class.java) { Message.parse("""{"v":4294967297,"t":"msg","id":"x","ts":1}""") }
        assertThrows(ProtoException::class.java) { Message.parse("""{"v":1,"t":"MSG","id":"x","ts":1}""") }
        assertThrows(ProtoException::class.java) { Message.parse("""{"v":1,"t":"","id":"x","ts":1}""") }
        assertThrows(ProtoException::class.java) { Message.parse("""{"v":1,"t":"msg","id":"","ts":1}""") }
        assertThrows(ProtoException::class.java) { Message.parse("""{"v":1,"t":"msg","ts":1}""") }
        for (t in listOf("msg", "alert", "hb", "pair")) assertEquals(t, Message.parse("""{"v":1,"t":"$t","id":"x"}""").t)
    }

    @Test
    fun notAnObjectOrTrailingData() {
        for (s in listOf("", "hello", "[]", "1", "\"x\"", "null", """{$base}x""", """{$base}{}""", """{"v":1""")) {
            assertThrows(s, ProtoException::class.java) { Message.parse(s) }
        }
        assertEquals("x", Message.parse(" \n{$base} \n\t").id)
    }

    @Test
    fun toStringHidesContent() {
        val m = Message.parse("""{$base,"title":"Alice","body":"secret text","url":"https://example.invalid/","tag":"19:abc"}""")
        val s = m.toString()
        for (part in listOf("Alice", "secret text", "example.invalid", "19:abc")) assertFalse(s, s.contains(part))
    }
}
