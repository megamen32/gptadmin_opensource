package com.gptadmin.cellularproxy;

import android.app.Activity;
import android.content.Intent;
import android.os.Bundle;

/** One-time app launch activates BOOT_COMPLETED delivery after installation. */
public final class MainActivity extends Activity {
    @Override public void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        startForegroundService(new Intent(this, CellularProxyService.class));
        finish();
    }
}
