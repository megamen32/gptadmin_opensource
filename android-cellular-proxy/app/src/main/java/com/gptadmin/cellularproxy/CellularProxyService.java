package com.gptadmin.cellularproxy;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.Service;
import android.content.Context;
import android.content.Intent;
import android.net.ConnectivityManager;
import android.net.LinkProperties;
import android.net.Network;
import android.net.NetworkCapabilities;
import android.net.NetworkRequest;
import android.content.pm.ServiceInfo;
import android.os.Build;
import android.os.IBinder;

import java.io.BufferedInputStream;
import java.io.BufferedOutputStream;
import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.InetAddress;
import java.net.InetSocketAddress;
import java.net.ServerSocket;
import java.net.Socket;
import java.nio.charset.StandardCharsets;
import java.util.concurrent.ArrayBlockingQueue;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Future;
import java.util.concurrent.RejectedExecutionException;
import java.util.concurrent.ThreadPoolExecutor;
import java.util.concurrent.TimeUnit;

/**
 * LAN ingress is available to the local Wi-Fi network. Every outbound target socket
 * is created through the Android cellular Network, even while Wi-Fi stays up.
 */
public final class CellularProxyService extends Service {
    static final int PROXY_PORT = 3126;
    private static final String CHANNEL = "cellular-proxy";
    private static final int IDLE_TIMEOUT_MS = 60_000;

    private final ExecutorService workers = new ThreadPoolExecutor(
            2, 8, 30, TimeUnit.SECONDS, new ArrayBlockingQueue<>(16), new ThreadPoolExecutor.AbortPolicy());
    private final ExecutorService relayWorkers = new ThreadPoolExecutor(
            8, 8, 30, TimeUnit.SECONDS, new ArrayBlockingQueue<>(8), new ThreadPoolExecutor.AbortPolicy());
    private volatile Network cellular;
    private volatile ServerSocket listener;
    private ConnectivityManager connectivity;
    private ConnectivityManager.NetworkCallback networkCallback;

    @Override public void onCreate() {
        super.onCreate();
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            startForeground(1, notification(), ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE);
        } else {
            startForeground(1, notification());
        }
        connectivity = getSystemService(ConnectivityManager.class);
        NetworkRequest request = new NetworkRequest.Builder()
                .addTransportType(NetworkCapabilities.TRANSPORT_CELLULAR)
                .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
                .build();
        networkCallback = new ConnectivityManager.NetworkCallback() {
            @Override public void onAvailable(Network network) { cellular = network; }
            @Override public void onLost(Network network) {
                if (network.equals(cellular)) cellular = null;
            }
        };
        connectivity.requestNetwork(request, networkCallback);
        workers.execute(this::serve);
    }

    @Override public int onStartCommand(Intent intent, int flags, int startId) {
        return START_STICKY;
    }

    @Override public void onDestroy() {
        closeQuietly(listener);
        if (connectivity != null && networkCallback != null) {
            connectivity.unregisterNetworkCallback(networkCallback);
        }
        workers.shutdownNow();
        relayWorkers.shutdownNow();
        super.onDestroy();
    }

    @Override public IBinder onBind(Intent intent) { return null; }

    private Notification notification() {
        NotificationManager manager = getSystemService(NotificationManager.class);
        manager.createNotificationChannel(new NotificationChannel(CHANNEL, "GPTAdmin cellular proxy", NotificationManager.IMPORTANCE_LOW));
        return new Notification.Builder(this, CHANNEL)
                .setContentTitle("GPTAdmin 4G proxy")
                .setContentText("LAN ingress; cellular egress")
                .setSmallIcon(android.R.drawable.ic_dialog_info)
                .build();
    }

    private void serve() {
        try (ServerSocket server = new ServerSocket()) {
            listener = server;
            server.setReuseAddress(true);
            server.bind(new InetSocketAddress("0.0.0.0", PROXY_PORT));
            while (!Thread.currentThread().isInterrupted()) {
                Socket client = server.accept();
                try {
                    workers.execute(() -> handle(client));
                } catch (RejectedExecutionException ignored) {
                    closeQuietly(client);
                }
            }
        } catch (IOException ignored) {
            // Shutdown closes the listener; Android restarts a sticky service when appropriate.
        }
    }

    private void handle(Socket client) {
        try (Socket closeClient = client;
             BufferedInputStream in = new BufferedInputStream(client.getInputStream());
             BufferedOutputStream out = new BufferedOutputStream(client.getOutputStream())) {
            client.setSoTimeout(IDLE_TIMEOUT_MS);
            int first = in.read();
            if (first == 5) {
                handleSocks5(in, out);
            } else if (first > 0) {
                handleConnect((byte) first, in, out);
            }
        } catch (IOException ignored) {
        }
    }

    private void handleSocks5(InputStream in, OutputStream out) throws IOException {
        int count = in.read();
        if (count < 0) return;
        readFully(in, count);
        out.write(new byte[]{5, 0});
        out.flush();
        if (in.read() != 5 || in.read() != 1 || in.read() != 0) return;
        int type = in.read();
        String host;
        if (type == 1) host = InetAddress.getByAddress(readFully(in, 4)).getHostAddress();
        else if (type == 4) host = InetAddress.getByAddress(readFully(in, 16)).getHostAddress();
        else if (type == 3) host = new String(readFully(in, requireByte(in)), StandardCharsets.UTF_8);
        else return;
        int port = (requireByte(in) << 8) | requireByte(in);
        Socket target = cellularSocket(host, port);
        out.write(new byte[]{5, 0, 0, 1, 0, 0, 0, 0, 0, 0});
        out.flush();
        relay(in, out, target);
    }

    private void handleConnect(byte first, InputStream in, OutputStream out) throws IOException {
        byte[] header = readHeader(first, in);
        String[] lines = new String(header, StandardCharsets.ISO_8859_1).split("\\r?\\n");
        String[] request = lines[0].split(" ");
        if (request.length == 3 && "GET".equals(request[0]) && "/health".equals(request[1])) {
            writeHealth(out);
            return;
        }
        if (request.length != 3 || !"CONNECT".equals(request[0])) {
            out.write("HTTP/1.1 405 Method Not Allowed\r\nConnection: close\r\n\r\n".getBytes(StandardCharsets.US_ASCII));
            out.flush();
            return;
        }
        String authority = request[1];
        int split = authority.lastIndexOf(':');
        if (split <= 0) throw new IOException("missing CONNECT port");
        Socket target = cellularSocket(authority.substring(0, split), Integer.parseInt(authority.substring(split + 1)));
        out.write("HTTP/1.1 200 Connection Established\r\n\r\n".getBytes(StandardCharsets.US_ASCII));
        out.flush();
        relay(in, out, target);
    }

    private void writeHealth(OutputStream out) throws IOException {
        Network network = cellular;
        LinkProperties properties = network == null ? null : connectivity.getLinkProperties(network);
        String iface = properties == null || properties.getInterfaceName() == null ? "unavailable" : properties.getInterfaceName();
        byte[] body = ("{\"transport\":\"cellular\",\"interface\":\"" + iface + "\"}\n")
                .getBytes(StandardCharsets.US_ASCII);
        out.write(("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: " + body.length
                + "\r\nConnection: close\r\n\r\n").getBytes(StandardCharsets.US_ASCII));
        out.write(body);
        out.flush();
    }

    private Socket cellularSocket(String host, int port) throws IOException {
        Network network = cellular;
        if (network == null) throw new IOException("cellular network unavailable");
        InetAddress[] addresses = network.getAllByName(host);
        IOException failure = null;
        for (InetAddress address : addresses) {
            Socket socket = null;
            try {
                socket = network.getSocketFactory().createSocket();
                socket.connect(new InetSocketAddress(address, port), 15_000);
                socket.setSoTimeout(IDLE_TIMEOUT_MS);
                return socket;
            } catch (IOException error) {
                closeQuietly(socket);
                failure = error;
            }
        }
        throw failure == null ? new IOException("no cellular address") : failure;
    }

    private void relay(InputStream clientIn, OutputStream clientOut, Socket target) throws IOException {
        try (Socket closeTarget = target;
             InputStream targetIn = target.getInputStream();
             OutputStream targetOut = target.getOutputStream()) {
            Future<?> downstream;
            try {
                downstream = relayWorkers.submit(() -> copy(targetIn, clientOut));
            } catch (RejectedExecutionException error) {
                throw new IOException("proxy busy", error);
            }
            copy(clientIn, targetOut);
            target.shutdownOutput();
            try {
                downstream.get();
            } catch (Exception ignored) {
            }
        }
    }

    private static void copy(InputStream in, OutputStream out) {
        try {
            byte[] buffer = new byte[32 * 1024];
            for (int n; (n = in.read(buffer)) >= 0; ) {
                out.write(buffer, 0, n);
                out.flush();
            }
        } catch (IOException ignored) {
        }
    }

    private static int requireByte(InputStream in) throws IOException {
        int value = in.read();
        if (value < 0) throw new IOException("unexpected EOF");
        return value;
    }

    private static byte[] readFully(InputStream in, int count) throws IOException {
        byte[] data = new byte[count];
        int offset = 0;
        while (offset < count) {
            int n = in.read(data, offset, count - offset);
            if (n < 0) throw new IOException("unexpected EOF");
            offset += n;
        }
        return data;
    }

    private static byte[] readHeader(byte first, InputStream in) throws IOException {
        ByteArrayOutputStream bytes = new ByteArrayOutputStream();
        bytes.write(first);
        int previous = -1, current;
        while (bytes.size() < 16 * 1024 && (current = in.read()) >= 0) {
            bytes.write(current);
            if (previous == '\r' && current == '\n') {
                byte[] body = bytes.toByteArray();
                int length = body.length;
                if (length >= 4 && body[length - 4] == '\r' && body[length - 3] == '\n') return body;
            }
            previous = current;
        }
        throw new IOException("invalid HTTP header");
    }

    private static void closeQuietly(ServerSocket socket) {
        if (socket == null) return;
        try { socket.close(); } catch (IOException ignored) { }
    }

    private static void closeQuietly(Socket socket) {
        if (socket == null) return;
        try { socket.close(); } catch (IOException ignored) { }
    }
}
