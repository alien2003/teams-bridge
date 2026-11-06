package io.github.alien2003.teamsbridge.proto

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/** PROTOCOL.md §7 rules 3 and 4. */
class ReplayFilterTest {
    private val ts = 1_767_225_600L
    private val day = Proto.MAX_AGE_MILLIS

    @Test
    fun ageEdge() {
        val f = ReplayFilter()
        assertTrue("exactly 24 h is still accepted", f.accept("a", ts, ts * 1000 + day))
        assertFalse("24 h + 1 ms is too old", f.accept("b", ts, ts * 1000 + day + 1))
        assertTrue("fresh", f.accept("c", ts, ts * 1000))
        assertTrue("from the future (clock skew) is accepted, like Go", f.accept("d", ts + 3600, ts * 1000))
        // A rejected (too old) message is not remembered: the same id may still arrive fresh.
        assertTrue(f.accept("b", ts, ts * 1000))
        assertEquals(listOf("a", "c", "d", "b"), f.snapshot())
    }

    @Test
    fun extremeTimestampsDoNotOverflow() {
        val now = ts * 1000
        assertTrue(ReplayFilter().accept("max", Long.MAX_VALUE, now))
        assertFalse(ReplayFilter().accept("min", Long.MIN_VALUE, now))
        assertFalse(ReplayFilter().accept("zero", 0, now))
        assertFalse(ReplayFilter().accept("neg", -1, now))
    }

    @Test
    fun duplicatesAreDropped() {
        val f = ReplayFilter()
        assertTrue(f.accept("id", ts, ts * 1000))
        assertFalse(f.accept("id", ts, ts * 1000))
        assertFalse(f.accept("id", ts + 10, ts * 1000 + 10_000))
        assertEquals(listOf("id"), f.snapshot())
    }

    @Test
    fun windowOf256() {
        val now = ts * 1000
        val f = ReplayFilter()
        for (i in 0 until Proto.REPLAY_SIZE) assertTrue(f.accept("id$i", ts, now))
        assertEquals(256, f.snapshot().size)
        for (i in 0 until Proto.REPLAY_SIZE) assertFalse("id$i is a duplicate", f.accept("id$i", ts, now))
        // The 257th id evicts the oldest one, which then counts as new again.
        assertTrue(f.accept("id256", ts, now))
        assertEquals(256, f.snapshot().size)
        assertEquals("id1", f.snapshot().first())
        assertFalse(f.accept("id1", ts, now))
        assertTrue(f.accept("id0", ts, now))
        assertEquals("id2", f.snapshot().first())
        assertEquals("id0", f.snapshot().last())
    }

    @Test
    fun persistedSnapshotRestoresTheWindow() {
        val now = ts * 1000
        val f = ReplayFilter()
        for (i in 0 until 300) f.accept("id$i", ts, now)
        val restored = ReplayFilter(f.snapshot())
        assertEquals(f.snapshot(), restored.snapshot())
        assertFalse(restored.accept("id299", ts, now))
        assertFalse(restored.accept("id44", ts, now))
        assertTrue(restored.accept("id43", ts, now))
        // A longer (or duplicated) stored list is trimmed to its newest 256 distinct ids.
        val long = List(400) { "x$it" }
        val trimmed = ReplayFilter(long + long.takeLast(3))
        assertEquals(256, trimmed.snapshot().size)
        assertTrue(trimmed.accept("x10", ts, now))
        assertFalse(trimmed.accept("x399", ts, now))
    }

    @Test
    fun messageOverload() {
        val m = Message(1, "msg", "m1", ts)
        val f = ReplayFilter()
        assertTrue(f.accept(m, ts * 1000))
        assertFalse(f.accept(m, ts * 1000))
    }
}
