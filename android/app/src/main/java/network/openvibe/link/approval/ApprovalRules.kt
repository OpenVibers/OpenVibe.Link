package network.openvibe.link.approval

/**
 * What an approval is about. Four of these are consequential enough that the phone asks for a fingerprint, a face or
 * the device credential before it sends an answer: money, passwords, deletions and a send to someone new (plan
 * 2026-11-07 §13.2: "Money, passwords, deletions and sends need a fingerprint or face confirmation").
 *
 * The category is the item's own `category` when the service sends one; otherwise it is read from the approval's
 * kind, title and summary. Actor's approvals today are all `codes.task`, whose title is the person's own sentence, so
 * the words are the only signal a phone has — hence the tests beside this file.
 */
enum class ApprovalCategory(val wire: String, val label: String, val needsBiometrics: Boolean) {
    MONEY("money", "Money", true),
    PASSWORD("password", "Passwords and codes", true),
    DELETE("delete", "Deleting", true),
    NEW_RECIPIENT("new_recipient", "A new recipient", true),
    OTHER("other", "Other", false);

    companion object {
        /** The service's own category, when it names one this app knows. */
        fun fromWire(value: String?): ApprovalCategory? =
            entries.firstOrNull { it.wire == value?.trim()?.lowercase() }
    }
}

object ApprovalRules {
    // Whole words only: "display" is not a payment and "monetary" is.
    private val MONEY = Regex(
        "\\b(money|payment|pay|paid|price|cost|charge|invoice|refund|purchase|buy|transfer|tip|deposit|withdraw|" +
            "subscription|billing|checkout|stripe|paypal|card|usd|eur|gbp)\\b|[$€£¥]|\\d+[.,]?\\d*\\s?(usd|eur|gbp)",
        RegexOption.IGNORE_CASE,
    )
    private val PASSWORD = Regex(
        "\\b(password|passcode|passphrase|secret|credential|credentials|api[ _-]?key|token|2fa|otp|one[- ]time " +
            "code|authenticator|login|sign[- ]in|private key|seed phrase)\\b",
        RegexOption.IGNORE_CASE,
    )
    private val DELETE = Regex(
        "\\b(delete|deletes|deleting|deleted|remove|removes|removing|erase|erasing|wipe|wiping|destroy|purge|" +
            "cancel (my )?(plan|account|subscription)|close (my )?account|unlink|drop)\\b",
        RegexOption.IGNORE_CASE,
    )
    private val NEW_RECIPIENT = Regex(
        "\\b(new (recipient|contact|person|address|number|email|phone)|" +
            "(send|share|email|mail|text|message|forward|invite|introduce)\\b[^.]{0,60}\\bto\\b|" +
            "reply to|invite)\\b",
        RegexOption.IGNORE_CASE,
    )

    /**
     * The category of one approval. An explicit category the service sent wins; otherwise the most consequential
     * reading of the words wins, so a sentence that both sends and pays is money, and a deletion is never shown as
     * something smaller.
     */
    fun classify(explicit: String?, kind: String?, title: String?, summary: String?): ApprovalCategory {
        ApprovalCategory.fromWire(explicit)?.let { return it }
        val text = listOfNotNull(kind, title, summary).joinToString(" ")
        if (text.isBlank()) return ApprovalCategory.OTHER
        return when {
            MONEY.containsMatchIn(text) -> ApprovalCategory.MONEY
            PASSWORD.containsMatchIn(text) -> ApprovalCategory.PASSWORD
            DELETE.containsMatchIn(text) -> ApprovalCategory.DELETE
            NEW_RECIPIENT.containsMatchIn(text) -> ApprovalCategory.NEW_RECIPIENT
            else -> ApprovalCategory.OTHER
        }
    }

    /** True when answering this one needs a fingerprint, a face or the device credential first. */
    fun needsBiometrics(explicit: String?, kind: String?, title: String?, summary: String?): Boolean =
        classify(explicit, kind, title, summary).needsBiometrics
}
