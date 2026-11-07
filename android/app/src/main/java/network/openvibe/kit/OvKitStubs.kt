// The check's stand-ins: what an app brings with it, not what the Compose artifacts carry.
// The kit's card view draws a picture through an image loader (Coil, Glide); the CI does not pull one in, it only
// needs the call to type-check.
package network.openvibe.kit

import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.layout.ContentScale

@Composable
fun AsyncImage(model: Any?, contentDescription: String?, modifier: Modifier = Modifier, contentScale: ContentScale = ContentScale.Fit) {
}
