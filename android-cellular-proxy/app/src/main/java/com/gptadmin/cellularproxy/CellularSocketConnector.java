package com.gptadmin.cellularproxy;

import java.io.IOException;
import java.net.InetAddress;
import java.net.Socket;
import java.util.ArrayList;
import java.util.HashSet;
import java.util.List;
import java.util.Set;

/** Selects cellular-only network handles and opens the requested destination socket. */
final class CellularSocketConnector {
    interface Candidate {
        String id();
        boolean isCellularInternet();
        boolean isValidated();
        InetAddress[] resolve(String host) throws IOException;
        Socket connect(InetAddress address, int port, int timeoutMillis) throws IOException;
    }

    interface Source {
        Candidate requestedNetwork();
        List<Candidate> currentNetworks();
    }

    private CellularSocketConnector() { }

    static Socket connect(String host, int port, int timeoutMillis, Source source) throws IOException {
        Set<String> attempted = new HashSet<>();
        IOException failure = null;
        Candidate requested = source.requestedNetwork();
        if (isEligible(requested) && attempted.add(requested.id())) {
            try {
                return connectOn(requested, host, port, timeoutMillis);
            } catch (IOException error) {
                failure = error;
            }
        }

        List<Candidate> current = source.currentNetworks();
        List<Candidate> fallback = current == null ? new ArrayList<>() : new ArrayList<>(current);
        for (int validationPass = 1; validationPass >= 0; validationPass--) {
            for (Candidate candidate : fallback) {
                if (!isEligible(candidate) || candidate.isValidated() != (validationPass == 1)
                        || !attempted.add(candidate.id())) continue;
                try {
                    return connectOn(candidate, host, port, timeoutMillis);
                } catch (IOException error) {
                    failure = error;
                }
            }
        }
        if (failure != null) throw failure;
        throw new IOException("cellular network unavailable");
    }

    private static Socket connectOn(Candidate network, String host, int port, int timeoutMillis) throws IOException {
        InetAddress[] addresses = network.resolve(host);
        if (addresses == null || addresses.length == 0) throw new IOException("no cellular address");
        IOException failure = null;
        for (InetAddress address : addresses) {
            try {
                return network.connect(address, port, timeoutMillis);
            } catch (IOException error) {
                failure = error;
            }
        }
        throw failure == null ? new IOException("no cellular address") : failure;
    }

    private static boolean isEligible(Candidate candidate) {
        return candidate != null && candidate.id() != null && candidate.isCellularInternet();
    }

    static boolean isBindDenied(IOException error) {
        for (Throwable current = error; current != null; current = current.getCause()) {
            String message = current.getMessage();
            if (message != null && message.contains("Binding socket to network")) return true;
        }
        return false;
    }
}
