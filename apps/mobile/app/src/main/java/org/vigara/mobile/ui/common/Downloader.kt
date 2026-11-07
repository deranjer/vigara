package org.vigara.mobile.ui.common

import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.runtime.Composable
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.ui.platform.LocalContext
import kotlinx.coroutines.launch
import kotlinx.serialization.json.JsonElement
import okhttp3.Response
import org.vigara.mobile.data.ApiException
import org.vigara.mobile.data.VigaraApi

class DownloadRequest(val method: String, val path: String, val body: JsonElement?, val onResponse: ((Response) -> Unit)?)

/**
 * Saves a server response to a file the user picks (Storage Access Framework), so the app needs no
 * storage permission. Call the returned function with the API path and a suggested file name.
 */
@Composable
fun rememberDownloader(api: VigaraApi, onResult: (String) -> Unit): (String, String, String, JsonElement?, ((Response) -> Unit)?) -> Unit {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    val pending = remember { mutableStateOf<DownloadRequest?>(null) }
    val launcher = rememberLauncherForActivityResult(ActivityResultContracts.CreateDocument("application/octet-stream")) { uri ->
        val request = pending.value
        pending.value = null
        if (uri == null || request == null) return@rememberLauncherForActivityResult
        scope.launch {
            try {
                api.download(request.method, request.path, request.body) { stream, response ->
                    request.onResponse?.invoke(response)
                    context.contentResolver.openOutputStream(uri)?.use { out -> stream.copyTo(out) }
                }
                onResult("Saved.")
            } catch (failure: ApiException) {
                onResult(failure.message ?: "Download failed.")
            }
        }
    }
    return { method, path, filename, body, onResponse ->
        pending.value = DownloadRequest(method, path, body, onResponse)
        launcher.launch(filename)
    }
}
