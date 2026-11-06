package io.github.alien2003.teamsbridge.core

import io.github.alien2003.teamsbridge.proto.Proto
import io.github.alien2003.teamsbridge.proto.Secret
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class WatchdogTest {
    private val h = 60L * 60 * 1000
    private val t0 = 1_767_225_600_000L

    @Test
    fun states() {
        assertEquals(Watchdog.Kind.UNPAIRED, Watchdog.state(t0, null, null).kind)
        // Before the first heartbeat the pairing time is the reference.
        assertEquals(Watchdog.Kind.WAITING, Watchdog.state(t0 + 11 * h, t0, null).kind)
        assertEquals(Watchdog.Kind.WAITING, Watchdog.state(t0 + 12 * h, t0, null).kind)
        assertEquals(Watchdog.Kind.STALE, Watchdog.state(t0 + 12 * h + 1, t0, null).kind)
        assertEquals(Watchdog.Kind.OK, Watchdog.state(t0 + 12 * h, t0, t0 + 6 * h).kind)
        assertEquals(Watchdog.Kind.OK, Watchdog.state(t0 + 18 * h, t0, t0 + 6 * h).kind)
        val stale = Watchdog.state(t0 + 19 * h, t0, t0 + 6 * h)
        assertEquals(Watchdog.Kind.STALE, stale.kind)
        assertEquals(13 * h, stale.silentForMillis)
        assertEquals(t0 + 6 * h, stale.referenceMillis)
        // A clock that went backwards never yields a negative silence.
        assertEquals(0L, Watchdog.state(t0, t0 + h, null).silentForMillis)
    }

    @Test
    fun alertAtMostOncePer12Hours() {
        val hb = t0
        assertFalse(Watchdog.shouldAlert(hb + 12 * h, t0, hb, null))
        val first = hb + 13 * h
        assertTrue(Watchdog.shouldAlert(first, t0, hb, null))
        assertFalse(Watchdog.shouldAlert(first + h, t0, hb, first))
        assertFalse(Watchdog.shouldAlert(first + 12 * h - 1, t0, hb, first))
        assertTrue(Watchdog.shouldAlert(first + 12 * h, t0, hb, first))
        // A heartbeat resets the state; a new silence after that still respects the 12 h spacing.
        val hb2 = first + 2 * h
        assertFalse(Watchdog.shouldAlert(hb2 + 5 * h, t0, hb2, first))
        assertFalse(Watchdog.shouldAlert(first + 12 * h - 1, t0, first - 1, first))
        // Unpaired: never.
        assertFalse(Watchdog.shouldAlert(t0 + 100 * h, null, null, null))
        // Clock moved back behind the last alert: alert rather than stay silent forever.
        assertTrue(Watchdog.shouldAlert(first - 1, t0 - 30 * h, null, first))
    }
}

class CodeMaskTest {
    private val code = "AAISEM2EKVTHPCEZVK54ZXPO74"
    private val display = "AAIS-EM2E-KVTH-PCEZ-VK54-ZXPO-74"

    @Test
    fun formatsAsYouType() {
        assertEquals("", CodeMask.format(""))
        assertEquals("A", CodeMask.format("a"))
        assertEquals("AAIS", CodeMask.format("aais"))
        assertEquals("AAIS-E", CodeMask.format("aaise"))
        assertEquals("AAIS-E", CodeMask.format("AAIS-E"))
        // Backspace right after a dash: the dash disappears instead of coming back.
        assertEquals("AAIS", CodeMask.format("AAIS-"))
        assertEquals(display, CodeMask.format(code))
        assertEquals(display, CodeMask.format(display.lowercase()))
    }

    @Test
    fun pasteAndGarbage() {
        assertEquals(display, CodeMask.format("tbd:v1:$code"))
        assertEquals(display, CodeMask.format("  TBD:V1:$code\n"))
        assertEquals(display, CodeMask.format("$code${"QQQQ"}")) // capped at 26 characters
        assertEquals("AAIS", CodeMask.format("a0a1i8s9!")) // non-base32 characters are dropped
        assertEquals("", CodeMask.format("01890-"))
    }

    @Test
    fun completeMaskParses() {
        val f = CodeMask.format(code)
        assertTrue(CodeMask.isComplete(f))
        assertFalse(CodeMask.isComplete(f.dropLast(1)))
        assertEquals(Secret.parse(code), Secret.parse(f))
    }
}

class UrlPolicyTest {
    @Test
    fun allowed() {
        for (u in listOf(
            "https://teams.microsoft.com/v2/?tbdChat=19%3Aabc%40thread.v2",
            "https://teams.microsoft.com/l/chat/19:abc@thread.v2/0?context=%7B%7D",
            "HTTPS://teams.microsoft.com/",
            "http://localhost:8080/x#y",
            "https://teams.cloud.microsoft",
        )) {
            assertEquals(u, u, UrlPolicy.sanitize(u))
        }
        assertEquals("https://teams.microsoft.com/", UrlPolicy.sanitize("  https://teams.microsoft.com/ "))
    }

    @Test
    fun everythingElseBecomesTheDefault() {
        for (u in listOf(
            null, "", "   ",
            "javascript:alert(1)",
            "intent://scan/#Intent;scheme=zxing;end",
            "file:///sdcard/x",
            "content://x/y",
            "tel:123",
            "https:teams.microsoft.com",
            "https://",
            "https:///path",
            "https://user@evil.example/",
            "https://teams.microsoft.com@evil.example/",
            "https://teams.microsoft.com/a b",
            "https://teams.microsoft.com/\nx",
            "https://teams.microsoft.com/\u0000",
            "ftp://teams.microsoft.com/",
            "//teams.microsoft.com/",
            "teams.microsoft.com",
            "https:\\\\evil.example",
        )) {
            assertEquals(u.toString(), Proto.DEFAULT_URL, UrlPolicy.sanitize(u))
        }
    }
}

class NotificationBudgetTest {
    private fun child(id: Int, group: String, t: Long) = NotificationBudget.Active(id, group, false, t, true)
    private fun summary(id: Int, group: String, t: Long) = NotificationBudget.Active(id, group, true, t, true)
    private fun alert(id: Int, t: Long) = NotificationBudget.Active(id, null, false, t, false)

    @Test
    fun underTheLimitNothingIsCancelled() {
        assertEquals(emptyList<Int>(), NotificationBudget.toCancel(emptyList(), "g", 4))
        // 2 active + a new child + a new summary = 4.
        assertEquals(emptyList<Int>(), NotificationBudget.toCancel(listOf(child(1, "a", 1), summary(2, "a", 1)), "g", 4))
        // The summary of "a" is already showing: only the child is new.
        val three = listOf(child(1, "a", 1), summary(2, "a", 1), child(3, "a", 2))
        assertEquals(emptyList<Int>(), NotificationBudget.toCancel(three, "a", 4))
    }

    @Test
    fun oldestMessagesGoFirstAndAlertsStay() {
        val active = listOf(alert(100, 0), child(1, "a", 10), child(2, "a", 30), summary(3, "a", 30), child(4, "b", 20), summary(5, "b", 20))
        // 6 active + 1 child into "a" (summary present) must fit 5: cancel 2, oldest first. The
        // oldest child is 1 ("a" keeps child 2); the next is 4, the last of "b", so its summary goes too.
        assertEquals(listOf(1, 4, 5), NotificationBudget.toCancel(active, "a", 5))
        // Alerts are never cancelled, even if nothing else can go.
        assertEquals(emptyList<Int>(), NotificationBudget.toCancel(listOf(alert(1, 0), alert(2, 0)), "a", 2))
    }

    @Test
    fun theTargetGroupKeepsItsSummary() {
        val active = listOf(child(1, "a", 10), summary(2, "a", 10))
        // Limit 2: posting one more child into "a" means dropping child 1, but summary 2 is reused.
        assertEquals(listOf(1), NotificationBudget.toCancel(active, "a", 2))
    }

    @Test
    fun orphanSummariesGoBeforeMessages() {
        // The user swiped away every message of "b"; its summary is left alone.
        val active = listOf(child(1, "a", 10), summary(2, "a", 10), summary(3, "b", 5))
        assertEquals(listOf(3), NotificationBudget.toCancel(active, "a", 3))
    }

    @Test
    fun aFullDayStaysUnderAndroidsCap() {
        // 60 chats with one message each: without trimming that would be 120 notifications.
        val active = ArrayList<NotificationBudget.Active>()
        var next = 1
        for (chat in 0 until 60) {
            val g = "g$chat"
            val cancel = NotificationBudget.toCancel(active, g).toSet()
            active.removeAll { it.id in cancel }
            active += child(next++, g, chat.toLong())
            if (active.none { it.summary && it.group == g }) active += summary(next++, g, chat.toLong())
            assertTrue("chat $chat: ${active.size}", active.size <= NotificationBudget.LIMIT)
            // Never a summary without its children.
            for (s in active.filter { it.summary }) assertTrue(active.any { !it.summary && it.group == s.group })
        }
        assertEquals(NotificationBudget.LIMIT, active.size)
        assertTrue(active.any { it.group == "g59" })
    }
}

class HandlerKindTest {
    @Test
    fun classify() {
        assertEquals(HandlerKind.BROWSER, HandlerKind.classify("com.android.chrome", false))
        assertEquals(HandlerKind.PWA, HandlerKind.classify("org.chromium.webapk.a1b2c3", true))
        assertEquals(HandlerKind.APP, HandlerKind.classify(HandlerKind.TEAMS_APP_PACKAGE, true))
        assertEquals(HandlerKind.BROWSER, HandlerKind.classify(HandlerKind.TEAMS_APP_PACKAGE, false))
        assertEquals("PWA", HandlerKind.PWA.label)
        assertEquals("Browser", HandlerKind.BROWSER.label)
        assertTrue(Proto.DEFAULT_URL in HandlerKind.QUERY_URIS)
    }
}
