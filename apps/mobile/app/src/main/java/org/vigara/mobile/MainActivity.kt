package org.vigara.mobile

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import org.vigara.mobile.ui.AppRoot
import org.vigara.mobile.ui.VigaraTheme

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        val api = (application as VigaraApp).api
        setContent { VigaraTheme { AppRoot(api) } }
    }
}
