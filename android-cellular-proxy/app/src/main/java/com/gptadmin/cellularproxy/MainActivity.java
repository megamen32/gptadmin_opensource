package com.gptadmin.cellularproxy;

import android.app.Activity;
import android.content.Intent;
import android.os.Build;
import android.os.Bundle;

/** One-time app launch activates BOOT_COMPLETED delivery after installation. */
public final class MainActivity extends Activity {
    @Override public void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        Intent service = new Intent(this, CellularProxyService.class);
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            startForegroundService(service);
        } else {
            startService(service);
        }
        finish();
    }
}
