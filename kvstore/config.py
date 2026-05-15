import os
from dataclasses import dataclass, field


@dataclass
class NodeConfig:
    node_id: int
    host: str = "0.0.0.0"
    port: int = 7001
    wal_dir: str = "./data"
    peers: list[str] = field(default_factory=list)  # ["host:port", ...]
    election_timeout_ms: int = 150
    heartbeat_interval_ms: int = 50
    ttl_cleanup_interval_s: float = 1.0


@dataclass
class ProxyConfig:
    host: str = "0.0.0.0"
    port: int = 8000
    nodes: list[str] = field(default_factory=list)  # ["host:port", ...]
    virtual_nodes: int = 100


def node_config_from_env() -> NodeConfig:
    return NodeConfig(
        node_id=int(os.environ.get("NODE_ID", "1")),
        host=os.environ.get("NODE_HOST", "0.0.0.0"),
        port=int(os.environ.get("NODE_PORT", "7001")),
        wal_dir=os.environ.get("WAL_DIR", "./data"),
        peers=os.environ.get("PEERS", "").split(",") if os.environ.get("PEERS") else [],
    )


def proxy_config_from_env() -> ProxyConfig:
    return ProxyConfig(
        host=os.environ.get("PROXY_HOST", "0.0.0.0"),
        port=int(os.environ.get("PROXY_PORT", "8000")),
        nodes=os.environ.get("NODES", "").split(",") if os.environ.get("NODES") else [],
    )
