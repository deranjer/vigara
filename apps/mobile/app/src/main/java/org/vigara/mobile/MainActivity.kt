package org.vigara.mobile

import android.content.Intent
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import org.vigara.mobile.push.Push
import org.vigara.mobile.ui.AppRoot
import org.vigara.mobile.ui.VigaraTheme

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        openFrom(intent)
        val api = (application as VigaraApp).api
        setContent { VigaraTheme { AppRoot(api) } }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        openFrom(intent)
    }

    /** A tapped alert notification opens the Alerts tab. */
    private fun openFrom(intent: Intent?) {
        intent?.getStringExtra(Push.EXTRA_OPEN)?.let { Push.openRequest.value = it }
        intent?.removeExtra(Push.EXTRA_OPEN)
    }
}
