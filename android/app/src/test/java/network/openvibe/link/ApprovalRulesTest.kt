package network.openvibe.link

import network.openvibe.link.approval.ApprovalCategory
import network.openvibe.link.approval.ApprovalRules
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class ApprovalRulesTest {
    @Test
    fun `money needs a second factor`() {
        assertEquals(ApprovalCategory.MONEY, ApprovalRules.classify(null, "codes.task", "Buy a laptop for $1,200", null))
        assertTrue(ApprovalRules.needsBiometrics(null, "codes.task", "Pay the hosting invoice", null))
        assertTrue(ApprovalRules.needsBiometrics(null, "codes.task", "Tip the streamer 5 EUR", null))
    }

    @Test
    fun `passwords and one-time codes need a second factor`() {
        assertEquals(ApprovalCategory.PASSWORD, ApprovalRules.classify(null, "codes.task", "Use the password vault", null))
        assertTrue(ApprovalRules.needsBiometrics(null, "codes.task", "Fill in the login form", null))
        assertTrue(ApprovalRules.needsBiometrics(null, "codes.task", "Read the 2FA code from the authenticator", null))
    }

    @Test
    fun `deleting needs a second factor`() {
        assertEquals(ApprovalCategory.DELETE, ApprovalRules.classify(null, "codes.task", "Delete the old repository", null))
        assertTrue(ApprovalRules.needsBiometrics(null, "codes.task", "Wipe the drive", null))
        assertTrue(ApprovalRules.needsBiometrics(null, "codes.task", "Cancel my subscription", null))
    }

    @Test
    fun `a send to someone new needs a second factor`() {
        assertEquals(ApprovalCategory.NEW_RECIPIENT, ApprovalRules.classify(null, "codes.task", "Send this to a new contact", null))
        assertTrue(ApprovalRules.needsBiometrics(null, "codes.task", "Email the report to Dana", null))
    }

    @Test
    fun `an ordinary task is answered without a prompt`() {
        assertEquals(ApprovalCategory.OTHER, ApprovalRules.classify(null, "codes.task", "Write a summary of the notes", null))
        assertFalse(ApprovalRules.needsBiometrics(null, "codes.task", "Write a summary of the notes", null))
        assertFalse(ApprovalRules.needsBiometrics(null, "codes.task", "Display the list", null))
    }

    @Test
    fun `the service's own category wins`() {
        assertEquals(ApprovalCategory.MONEY, ApprovalRules.classify("money", "codes.task", "Rename a file", null))
        assertEquals(ApprovalCategory.OTHER, ApprovalRules.classify("other", "codes.task", "Delete everything", null))
        assertFalse(ApprovalRules.needsBiometrics("other", "codes.task", "Pay the invoice", null))
    }

    @Test
    fun `an unknown category falls back to the words`() {
        assertEquals(ApprovalCategory.DELETE, ApprovalRules.classify("something_new", "codes.task", "Delete it", null))
    }

    @Test
    fun `the summary counts when the title is plain`() {
        assertEquals(
            ApprovalCategory.MONEY,
            ApprovalRules.classify(null, "approval", "A coding task", "It would spend 40 USD on the API"),
        )
    }

    @Test
    fun `nothing at all is other`() {
        assertEquals(ApprovalCategory.OTHER, ApprovalRules.classify(null, null, null, null))
        assertEquals(ApprovalCategory.OTHER, ApprovalRules.classify("  ", "", "", ""))
    }
}
