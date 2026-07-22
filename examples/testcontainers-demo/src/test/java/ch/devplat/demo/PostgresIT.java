package ch.devplat.demo;

import org.junit.jupiter.api.Test;
import org.testcontainers.containers.PostgreSQLContainer;
import org.testcontainers.junit.jupiter.Container;
import org.testcontainers.junit.jupiter.Testcontainers;

import java.sql.Connection;
import java.sql.DriverManager;
import java.sql.ResultSet;
import java.sql.Statement;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

// This is the actual end-to-end proof for `devplat connect`: Testcontainers
// reads DOCKER_HOST from the environment (set by the CLI to its local tunnel
// port), asks the remote VM's dockerd to start a container, gets back a
// mapped port on a random high port, and this test connects to it — same as
// it would against a local daemon. If this passes, the whole chain (agent
// proxy -> backend tunnel -> CLI port-mirroring) is proven, not just each
// piece in isolation.
@Testcontainers
class PostgresIT {

    @Container
    static final PostgreSQLContainer<?> postgres = new PostgreSQLContainer<>("postgres:16-alpine");

    @Test
    void queryThroughTheRemoteDaemon() throws Exception {
        try (Connection conn = DriverManager.getConnection(
                postgres.getJdbcUrl(), postgres.getUsername(), postgres.getPassword());
             Statement stmt = conn.createStatement();
             ResultSet rs = stmt.executeQuery("select 1")) {
            assertTrue(rs.next());
            assertEquals(1, rs.getInt(1));
        }
    }
}
