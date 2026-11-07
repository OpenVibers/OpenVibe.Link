// The OpenVibe kit for Jetpack Compose: a kit document, rendered.
//
// A kit document is a JSON tree of { type, props, children } (openvibe-shared/components). The web draws it with
// kit.js; this file draws the same document with the kit's own Compose views (OvKit.kt), wearing the theme's tokens
// (the Material 3 colour scheme and OvScale).
//
//   val doc = KitNode.parse(json)
//   MaterialTheme(colorScheme = VibeDarkColorScheme) {
//       KitView(doc) { action -> open(action) }
//   }
//
// The JSON is read with org.json, the parser Android carries in the platform.
package network.openvibe.kit

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.NavigationDrawerItem
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.text.font.FontWeight
import network.openvibe.theme.OvScale
import org.json.JSONArray
import org.json.JSONObject

/** What a tap asks the app to do: open a link, or run the action a button names. */
sealed class KitAction {
    /** Follow a link (a card, a row, a button with an href). */
    data class Open(val href: String) : KitAction()

    /** Run the action a button names (the app decides what that means). */
    data class Run(val id: String) : KitAction()

    /** Share a link: the app opens the platform's own share sheet (Android's chooser, with Direct Share). */
    data class Share(val title: String, val url: String) : KitAction()
}

/** A link the kit draws: where it goes and what it says. */
data class KitLink(val href: String, val label: String)

/** One tab: its key (what the web puts in the URL), its label and its count. */
data class KitTab(val key: String, val label: String, val count: Int? = null)

/** One row of a sidebar's navigation: where it goes, its name, its icon, whether you are on it, and a count. */
data class KitNav(val href: String, val label: String, val icon: String? = null, val here: Boolean = false, val count: Int? = null)

/** One number with its label. */
data class KitStat(val value: String, val label: String, val href: String? = null)

/**
 * A node of a kit document: one component, with its props and its children.
 *
 * The sealed cases are the kit's components; every one of them has a reference view in OvKit.kt that this file
 * draws with. A new component in components.js needs a case here (and a view), or the kit check fails.
 */
sealed class KitNode {
    /** The component's type, as it is written in the document. */
    abstract val type: String

    /** The component's children, in document order. */
    abstract val children: List<KitNode>

    /** A button: one action, a link or a run. */
    data class Button(
        val label: String,
        val href: String? = null,
        val icon: String? = null,
        val kind: OvKind = OvKind.Primary,
        val small: Boolean = false,
        override val children: List<KitNode> = emptyList(),
    ) : KitNode() {
        override val type: String = "button"
    }

    /** A card: one thing, the whole card is one link. */
    data class Card(
        val href: String,
        val title: String,
        val variant: String? = null,
        val ratio: Float? = null,
        val image: String? = null,
        val alt: String? = null,
        val badge: String? = null,
        val live: Boolean = false,
        val corner: String? = null,
        val sub: String? = null,
        val preview: String? = null,
        val who: String? = null,
        val avatar: String? = null,
        val count: String? = null,
        val shownAt: String? = null,
        val level: Int? = null,
        override val children: List<KitNode> = emptyList(),
    ) : KitNode() {
        override val type: String = "card"

        /** The media's shape, as the view takes it. */
        val ratioValue: Float get() = ratio ?: 16f / 9f

        /** The line under the title: who, the count, when. */
        val metaText: String?
            get() = listOfNotNull(who, count, shownAt).joinToString(" · ").ifEmpty { null }

        /** The badge: the one the document gives, or LIVE. */
        val badgeText: String? get() = badge ?: if (live) "LIVE" else null
    }

    /** A rail: a row of cards that scrolls sideways. */
    data class Rail(
        val title: String,
        val id: String? = null,
        val more: KitLink? = null,
        val size: String = "m",
        val wrap: Boolean = false,
        val label: String? = null,
        override val children: List<KitNode> = emptyList(),
    ) : KitNode() {
        override val type: String = "rail"

        /** The cards the rail holds. */
        val cards: List<Card> get() = children.filterIsInstance<Card>()
    }

    /** A row: one item in a list. */
    data class Row(
        val title: String,
        val href: String? = null,
        val rel: String? = null,
        val meta: String? = null,
        val sub: String? = null,
        val mark: String? = null,
        val unread: Int? = null,
        override val children: List<KitNode> = emptyList(),
    ) : KitNode() {
        override val type: String = "row"
    }

    /** A list: rows in one rounded panel. */
    data class ListPanel(
        val title: String? = null,
        val more: KitLink? = null,
        val empty: String? = null,
        val flush: Boolean = false,
        override val children: List<KitNode> = emptyList(),
    ) : KitNode() {
        override val type: String = "list"

        /** The rows the list holds. */
        val rows: List<Row> get() = children.filterIsInstance<Row>()
    }

    /** A section: a titled group of anything. */
    data class Section(
        val title: String,
        val id: String? = null,
        val count: Int? = null,
        val note: String? = null,
        override val children: List<KitNode> = emptyList(),
    ) : KitNode() {
        override val type: String = "section"
    }

    /** A fold: a group that opens and closes. */
    data class Fold(
        val title: String,
        val id: String? = null,
        val meta: String? = null,
        val open: Boolean = false,
        val tone: String? = null,
        override val children: List<KitNode> = emptyList(),
    ) : KitNode() {
        override val type: String = "fold"
    }

    /** Tabs: the sections of a screen, one at a time. */
    data class Tabs(
        val items: List<KitTab> = emptyList(),
        val base: String? = null,
        val active: String? = null,
        val param: String = "tab",
        val label: String = "Sections",
        override val children: List<KitNode> = emptyList(),
    ) : KitNode() {
        override val type: String = "tabs"

        /** The tab that is open: the one the document names, or the first. */
        val selectedIndex: Int
            get() {
                val i = items.indexOfFirst { it.key == active }
                return if (i >= 0) i else 0
            }
    }

    /** Sidebar: an app's navigation, as rows (on a phone the app shows it in its drawer). */
    data class Sidebar(
        val label: String = "Navigation",
        val nav: List<KitNav> = emptyList(),
        override val children: List<KitNode> = emptyList(),
    ) : KitNode() {
        override val type: String = "sidebar"
    }

    /** Stats: the numbers, in a row. */
    data class Stats(
        val items: List<KitStat> = emptyList(),
        val label: String = "In numbers",
        override val children: List<KitNode> = emptyList(),
    ) : KitNode() {
        override val type: String = "stats"
    }

    /** A notice: one line that says how things stand. */
    data class Notice(
        val text: String,
        val kind: OvNoticeKind = OvNoticeKind.Info,
        override val children: List<KitNode> = emptyList(),
    ) : KitNode() {
        override val type: String = "notice"
    }

    /** The Share button: the platform's own share sheet for the link (Android's chooser, with Direct Share). */
    data class Share(
        val title: String,
        val url: String? = null,
        val label: String = "Share",
        override val children: List<KitNode> = emptyList(),
    ) : KitNode() {
        override val type: String = "share"
    }

    /** An empty state: what a screen shows when it has nothing to show. */
    data class Empty(
        val title: String,
        val text: String? = null,
        val icon: String? = null,
        val action: KitLink? = null,
        override val children: List<KitNode> = emptyList(),
    ) : KitNode() {
        override val type: String = "empty"
    }

    companion object {
        /** A document, as JSON, into its nodes. Throws when a node is not a component the kit knows. */
        fun parse(json: String): KitNode = parse(JSONObject(json))

        /** A node, as JSON, into its component. Throws when the type is not one the kit knows. */
        fun parse(obj: JSONObject): KitNode {
            val type = obj.optString("type", "")
            val props = obj.optJSONObject("props") ?: JSONObject()
            val kids = childrenOf(obj.optJSONArray("children"))
            return when (type) {
                "button" -> Button(
                    label = props.str("label") ?: "",
                    href = props.str("href"),
                    icon = props.str("icon"),
                    kind = when (props.str("kind")) {
                        "quiet" -> OvKind.Quiet
                        "danger" -> OvKind.Danger
                        else -> OvKind.Primary
                    },
                    small = props.bool("small"),
                )
                "card" -> Card(
                    href = props.str("href") ?: "",
                    title = props.str("title") ?: "",
                    variant = props.str("type"),
                    ratio = ratioOf(props.str("ratio")),
                    image = props.str("image"),
                    alt = props.str("alt"),
                    badge = props.str("badge"),
                    live = props.bool("live"),
                    corner = props.str("corner"),
                    sub = props.str("sub"),
                    preview = props.str("preview"),
                    who = props.str("who"),
                    avatar = props.str("avatar"),
                    count = props.count("count"),
                    shownAt = props.str("when"),
                    level = props.int("level"),
                )
                "rail" -> Rail(
                    title = props.str("title") ?: "",
                    id = props.str("id"),
                    more = props.link("more"),
                    size = props.str("size") ?: "m",
                    wrap = props.bool("wrap"),
                    label = props.str("label"),
                    children = kids,
                )
                "row" -> Row(
                    title = props.str("title") ?: "",
                    href = props.str("href"),
                    rel = props.str("rel"),
                    meta = props.str("meta"),
                    sub = props.str("sub"),
                    mark = props.str("mark"),
                    unread = props.int("unread"),
                )
                "list" -> ListPanel(
                    title = props.str("title"),
                    more = props.link("more"),
                    empty = props.str("empty"),
                    flush = props.bool("flush"),
                    children = kids,
                )
                "section" -> Section(
                    title = props.str("title") ?: "",
                    id = props.str("id"),
                    count = props.int("count"),
                    note = props.str("note"),
                    children = kids,
                )
                "fold" -> Fold(
                    title = props.str("title") ?: "",
                    id = props.str("id"),
                    meta = props.str("meta"),
                    open = props.bool("open"),
                    tone = props.str("tone"),
                    children = kids,
                )
                "tabs" -> Tabs(
                    items = tabsOf(props.array("items")),
                    base = props.str("base"),
                    active = props.str("active"),
                    param = props.str("param") ?: "tab",
                    label = props.str("label") ?: "Sections",
                )
                "sidebar" -> Sidebar(
                    label = props.str("label") ?: "Navigation",
                    nav = navsOf(props.array("nav")),
                )
                "stats" -> Stats(
                    items = statsOf(props.array("items")),
                    label = props.str("label") ?: "In numbers",
                )
                "notice" -> Notice(
                    text = props.str("text") ?: "",
                    kind = when (props.str("kind")) {
                        "ok" -> OvNoticeKind.Ok
                        "warn" -> OvNoticeKind.Warn
                        "error" -> OvNoticeKind.Error
                        else -> OvNoticeKind.Info
                    },
                )
                "share" -> Share(
                    title = props.str("title") ?: "",
                    url = props.str("url"),
                    label = props.str("label") ?: "Share",
                )
                "empty" -> Empty(
                    title = props.str("title") ?: "",
                    text = props.str("text"),
                    icon = props.str("icon"),
                    action = props.link("action"),
                )
                else -> throw IllegalArgumentException("unknown kit component: " + type)
            }
        }

        /** The children of a node, parsed. */
        private fun childrenOf(array: JSONArray?): List<KitNode> {
            if (array == null) return emptyList()
            val out = ArrayList<KitNode>()
            for (i in 0 until array.length()) {
                val child = array.optJSONObject(i) ?: continue
                out.add(parse(child))
            }
            return out
        }

        /** The tabs of a tab row. */
        private fun tabsOf(array: JSONArray?): List<KitTab> {
            if (array == null) return emptyList()
            val out = ArrayList<KitTab>()
            for (i in 0 until array.length()) {
                val tab = array.optJSONObject(i) ?: continue
                val key = tab.str("key") ?: continue
                out.add(KitTab(key, tab.str("label") ?: key, tab.int("count")))
            }
            return out
        }

        /** The rows of a sidebar's navigation. */
        private fun navsOf(array: JSONArray?): List<KitNav> {
            if (array == null) return emptyList()
            val out = ArrayList<KitNav>()
            for (i in 0 until array.length()) {
                val row = array.optJSONObject(i) ?: continue
                val href = row.str("href") ?: continue
                out.add(KitNav(href, row.str("label") ?: href, row.str("icon"), row.bool("here"), row.int("count")))
            }
            return out
        }

        /** The numbers of a stats row. */
        private fun statsOf(array: JSONArray?): List<KitStat> {
            if (array == null) return emptyList()
            val out = ArrayList<KitStat>()
            for (i in 0 until array.length()) {
                val row = array.optJSONArray(i) ?: continue
                val value = row.optString(0, "")
                val label = row.optString(1, "")
                val href = if (row.length() > 2 && !row.isNull(2)) row.optString(2, "") else null
                out.add(KitStat(value, label, href))
            }
            return out
        }
    }
}

// ── Reading the props ──

/** A string prop, or nothing when the document does not carry it. */
private fun JSONObject.str(key: String): String? = if (has(key) && !isNull(key)) optString(key, "") else null

/** A boolean prop. */
private fun JSONObject.bool(key: String, def: Boolean = false): Boolean =
    if (has(key) && !isNull(key)) optBoolean(key, def) else def

/** A number prop. */
private fun JSONObject.int(key: String): Int? = if (has(key) && !isNull(key)) optInt(key) else null

/** An array prop. */
private fun JSONObject.array(key: String): JSONArray? = if (has(key) && !isNull(key)) optJSONArray(key) else null

/** A link prop: { href, label }. */
private fun JSONObject.link(key: String): KitLink? {
    val obj = optJSONObject(key) ?: return null
    val href = obj.str("href") ?: return null
    return KitLink(href, obj.str("label") ?: href)
}

/** A count prop: a number, or [number, word]. */
private fun JSONObject.count(key: String): String? {
    val value = opt(key) ?: return null
    return when (value) {
        is Number -> value.toString()
        is String -> value
        is JSONArray -> {
            val number = value.opt(0)
            val word = value.optString(1, "")
            if (number == null) null else if (word.isEmpty()) number.toString() else number.toString() + " " + word
        }
        else -> null
    }
}

/** A ratio prop: "16:9" and the like, as the number the view takes. */
private fun ratioOf(raw: String?): Float? {
    if (raw == null) return null
    val parts = raw.split(":")
    if (parts.size == 2) {
        val a = parts[0].trim().toFloatOrNull()
        val b = parts[1].trim().toFloatOrNull()
        if (a != null && b != null && b != 0f) return a / b
    }
    return raw.trim().toFloatOrNull()
}

// ── Drawing it ──

/**
 * A kit document, drawn.
 *
 * The app wears the theme's Material 3 colour scheme around it (see the kit's preamble), and this draws every
 * component with the kit's own views. A tap comes back as a KitAction, so the app decides what a link or a button
 * does.
 */
@Composable
fun KitView(
    doc: KitNode,
    icons: Map<String, ImageVector> = emptyMap(),
    onAction: (KitAction) -> Unit = {},
) {
    Column(Modifier.fillMaxWidth()) {
        KitNodeView(doc, icons, onAction)
    }
}

/** One node, drawn with the kit's view for its component. */
@Composable
fun KitNodeView(
    node: KitNode,
    icons: Map<String, ImageVector> = emptyMap(),
    onAction: (KitAction) -> Unit = {},
) {
    when (node) {
        is KitNode.Button -> OvButton(
            label = node.label,
            onClick = {
                val href = node.href
                if (href != null) onAction(KitAction.Open(href)) else onAction(KitAction.Run(node.label))
            },
            kind = node.kind,
            icon = node.icon?.let { icons[it] },
        )
        is KitNode.Card -> OvCard(
            title = node.title,
            onOpen = { onAction(KitAction.Open(node.href)) },
            image = node.image,
            ratio = node.ratioValue,
            badge = node.badgeText,
            sub = node.sub,
            meta = node.metaText,
        )
        is KitNode.Rail -> OvRail(
            title = node.title,
            seeAll = node.more?.let { more ->
                val href = more.href
                { onAction(KitAction.Open(href)) }
            },
        ) {
            items(node.cards) { card ->
                KitNodeView(card, icons, onAction)
            }
        }
        is KitNode.Row -> {
            val href = node.href
            val open: (() -> Unit)? = if (href != null) {
                val link = href
                { onAction(KitAction.Open(link)) }
            } else {
                null
            }
            val markName = node.mark
            val mark: (@Composable () -> Unit)? = if (markName != null) {
                val name = markName
                val slot: @Composable () -> Unit = { OvInitials(name) }
                slot
            } else {
                null
            }
            val unread = node.unread
            val trail: (@Composable () -> Unit)? = if (unread != null && unread > 0) {
                val count = unread
                val slot: @Composable () -> Unit = {
                    Text(
                        count.toString(),
                        color = MaterialTheme.colorScheme.primary,
                        fontWeight = FontWeight.Bold,
                        fontSize = OvScale.TypeSizeMeta,
                    )
                }
                slot
            } else {
                null
            }
            OvRow(
                title = node.title,
                onOpen = open,
                sub = node.sub,
                meta = node.meta,
                mark = mark,
                trail = trail,
            )
        }
        is KitNode.ListPanel -> OvList(title = node.title) {
            val rows = node.rows
            if (rows.isEmpty()) {
                val empty = node.empty
                if (empty != null) OvEmpty(empty)
            } else {
                rows.forEach { row ->
                    KitNodeView(row, icons, onAction)
                }
            }
        }
        is KitNode.Section -> OvSection(title = node.title, note = node.note) {
            node.children.forEach { child ->
                KitNodeView(child, icons, onAction)
            }
        }
        is KitNode.Fold -> OvFold(title = node.title, meta = node.meta, initiallyOpen = node.open) {
            node.children.forEach { child ->
                KitNodeView(child, icons, onAction)
            }
        }
        is KitNode.Tabs -> {
            val labels = node.items.map { it.label }
            var selected by rememberSaveable { mutableStateOf(node.selectedIndex) }
            OvTabs(tabs = labels, selected = selected, onSelect = { selected = it })
        }
        is KitNode.Sidebar -> OvSidebarNav(node, icons, onAction)
        is KitNode.Stats -> OvStats(items = node.items.map { it.value to it.label })
        is KitNode.Notice -> OvNotice(text = node.text, kind = node.kind)
        is KitNode.Share -> OvShare(title = node.title, url = node.url ?: "", label = node.label, onShare = { t, u -> onAction(KitAction.Share(t, u)) })
        is KitNode.Empty -> {
            val link = node.action
            val action: Pair<String, () -> Unit>? = if (link != null) {
                val href = link.href
                val label = link.label
                label to { onAction(KitAction.Open(href)) }
            } else {
                null
            }
            OvEmpty(title = node.title, text = node.text, action = action)
        }
    }
}

/**
 * A row's mark: up to two letters on the theme's accent, when the document gives a name and no picture.
 */
@Composable
private fun OvInitials(name: String) {
    val letters = name.trim().take(2).uppercase()
    Surface(
        shape = RoundedCornerShape(OvScale.RadiusPill),
        color = MaterialTheme.colorScheme.primaryContainer,
    ) {
        Text(
            letters,
            Modifier.padding(horizontal = OvScale.SpaceSm, vertical = OvScale.SpaceXs),
            color = MaterialTheme.colorScheme.onPrimaryContainer,
            fontSize = OvScale.TypeSizeCaption,
            fontWeight = FontWeight.Bold,
        )
    }
}

/** A sidebar's navigation, drawn as a drawer's rows: the one you are on is selected, and a tap opens its link. */
@Composable
fun OvSidebarNav(node: KitNode.Sidebar, icons: Map<String, ImageVector>, onAction: (KitAction) -> Unit) {
    Column(Modifier.fillMaxWidth().padding(OvScale.SpaceMd)) {
        node.nav.forEach { item ->
            val vector = item.icon?.let { icons[it] }
            val count = item.count
            NavigationDrawerItem(
                label = { Text(item.label) },
                selected = item.here,
                onClick = { onAction(KitAction.Open(item.href)) },
                icon = if (vector != null) ({ Icon(vector, contentDescription = null) }) else null,
                badge = if (count != null && count > 0) ({ Text(count.toString()) }) else null,
                shape = RoundedCornerShape(OvScale.RadiusMd),
            )
        }
    }
}
