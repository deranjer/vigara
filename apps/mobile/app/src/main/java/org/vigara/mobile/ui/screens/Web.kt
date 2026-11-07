package org.vigara.mobile.ui.screens

import android.annotation.SuppressLint
import android.graphics.Bitmap
import android.view.ViewGroup
import android.webkit.CookieManager
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.activity.compose.BackHandler
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.viewinterop.AndroidView
import org.vigara.mobile.data.VigaraApi
import org.vigara.mobile.ui.common.AppScaffold

/** What a [WebScreen] loads: a path on the Vigara server, or an auto-submitting POST form (GitHub App setup). */
sealed interface WebTarget {
    data class ServerPath(val path: String) : WebTarget
    data class Url(val url: String) : WebTarget
    data class PostForm(val action: String, val field: String, val value: String) : WebTarget
}

private fun escape(value: String) = value.replace("&", "&amp;").replace("\"", "&quot;").replace("<", "&lt;").replace(">", "&gt;")

/**
 * Shows a server page inside the app, sharing the sign-in session with it. Used for the few web-only
 * flows that redirect through other sites: connected MCP applications and GitHub App registration.
 */
@SuppressLint("SetJavaScriptEnabled")
@Composable
fun WebScreen(api: VigaraApi, title: String, target: WebTarget, onBack: () -> Unit) {
    var webView by remember { mutableStateOf<WebView?>(null) }
    var canGoBack by remember { mutableStateOf(false) }
    BackHandler(enabled = canGoBack) { webView?.goBack() }
    DisposableEffect(Unit) { onDispose { webView?.destroy() } }

    AppScaffold(title, onBack = onBack, actions = { TextButton(onBack) { Text("Done") } }) { padding ->
        AndroidView(
            modifier = Modifier.padding(padding).fillMaxSize(),
            factory = { context ->
                val cookies = CookieManager.getInstance()
                cookies.setAcceptCookie(true)
                api.cookiesFor("/").forEach { cookies.setCookie(api.serverUrl, it.toString()) }
                cookies.flush()
                WebView(context).apply {
                    layoutParams = ViewGroup.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT)
                    settings.javaScriptEnabled = true
                    settings.domStorageEnabled = true
                    webViewClient = object : WebViewClient() {
                        override fun onPageStarted(view: WebView, url: String?, favicon: Bitmap?) { canGoBack = view.canGoBack() }
                        override fun onPageFinished(view: WebView, url: String?) { canGoBack = view.canGoBack() }
                    }
                    when (target) {
                        is WebTarget.ServerPath -> loadUrl(api.serverUrl + target.path)
                        is WebTarget.Url -> loadUrl(target.url)
                        is WebTarget.PostForm -> loadDataWithBaseURL(
                            api.serverUrl,
                            "<html><body onload=\"document.forms[0].submit()\"><form method=\"post\" action=\"${escape(target.action)}\">" +
                                "<input type=\"hidden\" name=\"${escape(target.field)}\" value=\"${escape(target.value)}\"/></form>Redirecting…</body></html>",
                            "text/html", "utf-8", null,
                        )
                    }
                    webView = this
                }
            },
        )
    }
}
