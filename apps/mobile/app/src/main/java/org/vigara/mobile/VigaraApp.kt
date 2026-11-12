package org.vigara.mobile

import android.app.Application
import org.vigara.mobile.data.VigaraApi

class VigaraApp : Application() {
    lateinit var api: VigaraApi
        private set

    override fun onCreate() {
        super.onCreate()
        api = VigaraApi(this)
        org.vigara.mobile.push.Push.createChannels(this)
        org.vigara.mobile.push.Push.load(this)
    }
}
