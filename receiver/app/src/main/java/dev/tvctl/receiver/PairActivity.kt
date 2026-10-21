package dev.tvctl.receiver

import android.os.Bundle
import android.widget.TextView
import androidx.appcompat.app.AppCompatActivity

/** Full-screen PIN, mirroring the Fire TV's own pairing dialog. */
class PairActivity : AppCompatActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_pair)
        findViewById<TextView>(R.id.pin).text = intent.getStringExtra("pin") ?: "----"
        current = this
    }

    override fun onDestroy() {
        if (current === this) current = null
        super.onDestroy()
    }

    companion object {
        @Volatile private var current: PairActivity? = null
        fun dismiss() = current?.finish()
    }
}
