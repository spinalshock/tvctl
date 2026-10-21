package dev.tvctl.receiver

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

/**
 * Starts the service at boot and after an update, the same way Netflix and
 * Spotify come up without being opened. Fire OS delivers BOOT_COMPLETED to
 * sideloaded apps as long as they have been launched once since install.
 */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        ReceiverService.start(context)
    }
}
