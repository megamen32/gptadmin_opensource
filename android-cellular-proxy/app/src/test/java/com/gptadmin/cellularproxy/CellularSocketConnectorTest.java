package com.gptadmin.cellularproxy;

import org.junit.Test;

import java.io.IOException;
import java.net.InetAddress;
import java.net.Socket;
import java.util.List;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertSame;
import static org.junit.Assert.fail;

public final class CellularSocketConnectorTest {
    @Test public void staleRequestedHandleFallsBackToFreshCellularAndNeverWifi() throws Exception {
        FakeCandidate stale = new FakeCandidate("cell-old", true, true, new IOException("stale handle"));
        FakeCandidate fresh = new FakeCandidate("cell-new", true, true, null);
        FakeCandidate wifi = new FakeCandidate("wifi", false, true, null);

        Socket socket = CellularSocketConnector.connect("example.test", 443, 15000,
                new FakeSource(stale, List.of(stale, fresh, wifi)));

        assertSame(fresh.socket, socket);
        assertEquals(1, stale.connectCalls);
        assertEquals(1, fresh.connectCalls);
        assertEquals(0, wifi.connectCalls);
    }

    @Test public void wifiOnlyIsNeverUsedAsFallback() throws Exception {
        FakeCandidate wifi = new FakeCandidate("wifi", false, true, null);
        try {
            CellularSocketConnector.connect("example.test", 443, 15000,
                    new FakeSource(null, List.of(wifi)));
            fail("expected no cellular network");
        } catch (IOException expected) {
            assertEquals("cellular network unavailable", expected.getMessage());
        }
        assertEquals(0, wifi.connectCalls);
    }

    @Test public void validatedCellularNetworkWinsAndWifiIsIgnored() throws Exception {
        FakeCandidate unvalidated = new FakeCandidate("cell-unvalidated", true, false, null);
        FakeCandidate validated = new FakeCandidate("cell-validated", true, true, null);
        FakeCandidate wifi = new FakeCandidate("wifi", false, true, null);

        Socket socket = CellularSocketConnector.connect("example.test", 443, 15000,
                new FakeSource(null, List.of(unvalidated, wifi, validated)));

        assertSame(validated.socket, socket);
        assertEquals(0, unvalidated.connectCalls);
        assertEquals(0, wifi.connectCalls);
    }

    @Test public void bindDenialOnStaleHandleStillRetriesFreshCellularNetwork() throws Exception {
        FakeCandidate blocked = new FakeCandidate("cell-blocked", true, true,
                new IOException("Binding socket to network 100 failed: EPERM"));
        FakeCandidate second = new FakeCandidate("cell-second", true, true, null);

        Socket socket = CellularSocketConnector.connect("example.test", 443, 15000,
                new FakeSource(blocked, List.of(blocked, second)));

        assertSame(second.socket, socket);
        assertEquals(1, second.connectCalls);
    }

    @Test public void vpnBindDenialIsReportedWhenAllCellularNetworksRejectBinding() throws Exception {
        FakeCandidate blocked = new FakeCandidate("cell-blocked", true, true,
                new IOException("Binding socket to network 100 failed: EPERM"));
        FakeCandidate second = new FakeCandidate("cell-second", true, true,
                new IOException("Binding socket to network 101 failed: EPERM"));

        try {
            CellularSocketConnector.connect("example.test", 443, 15000,
                    new FakeSource(blocked, List.of(blocked, second)));
            fail("expected VPN network binding refusal");
        } catch (IOException expected) {
            org.junit.Assert.assertTrue(CellularSocketConnector.isBindDenied(expected));
        }
        assertEquals(1, second.connectCalls);
    }

    @Test public void dnsFailureOnStaleNetworkRetriesCurrentCellularNetwork() throws Exception {
        FakeCandidate stale = new FakeCandidate("cell-old", true, true, null);
        stale.resolveFailure = new IOException("stale cellular DNS handle");
        FakeCandidate fresh = new FakeCandidate("cell-new", true, true, null);

        Socket socket = CellularSocketConnector.connect("example.test", 443, 15000,
                new FakeSource(stale, List.of(stale, fresh)));

        assertSame(fresh.socket, socket);
        assertEquals(1, stale.resolveCalls);
        assertEquals(1, fresh.connectCalls);
    }

    private static final class FakeSource implements CellularSocketConnector.Source {
        private final CellularSocketConnector.Candidate requested;
        private final List<CellularSocketConnector.Candidate> current;

        FakeSource(CellularSocketConnector.Candidate requested, List<CellularSocketConnector.Candidate> current) {
            this.requested = requested;
            this.current = current;
        }

        @Override public CellularSocketConnector.Candidate requestedNetwork() { return requested; }
        @Override public List<CellularSocketConnector.Candidate> currentNetworks() { return current; }
    }

    private static final class FakeCandidate implements CellularSocketConnector.Candidate {
        private final String id;
        private final boolean cellularInternet;
        private final boolean validated;
        private final IOException connectFailure;
        private final Socket socket = new Socket();
        private IOException resolveFailure;
        private int connectCalls;
        private int resolveCalls;

        FakeCandidate(String id, boolean cellularInternet, boolean validated, IOException connectFailure) {
            this.id = id;
            this.cellularInternet = cellularInternet;
            this.validated = validated;
            this.connectFailure = connectFailure;
        }

        @Override public String id() { return id; }
        @Override public boolean isCellularInternet() { return cellularInternet; }
        @Override public boolean isValidated() { return validated; }
        @Override public InetAddress[] resolve(String host) throws IOException {
            resolveCalls++;
            if (resolveFailure != null) throw resolveFailure;
            return new InetAddress[]{InetAddress.getByAddress(new byte[]{(byte) 192, 0, 2, 1})};
        }
        @Override public Socket connect(InetAddress address, int port, int timeoutMillis) throws IOException {
            connectCalls++;
            if (connectFailure != null) throw connectFailure;
            return socket;
        }
    }
}
