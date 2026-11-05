# The admin console listens on port 9090, apart from the API, so a firewall can keep it
# on the internal network.
ADMIN_PORT = 9090


class Console:
    """The admin console's settings, which its server reads at start."""

    PORT = 9090
    BIND = "127.0.0.1"
