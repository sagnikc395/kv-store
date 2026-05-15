#!/usr/bin/env python3
"""
Launch the consistent-hashing proxy.

Usage:
    python run_proxy.py --port=8000 --nodes=localhost:7001,localhost:7002,localhost:7003
"""

import argparse
import logging
import threading
from xmlrpc.server import SimpleXMLRPCServer

from kvstore.proxy import HashRing, ProxyHandler

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
log = logging.getLogger(__name__)


def parse_args() -> argparse.Namespace:
    p = argparse.ArgumentParser(description="Run the kv-store proxy")
    p.add_argument("--port", type=int, default=8000, help="Listen port")
    p.add_argument(
        "--nodes",
        default="",
        help="Comma-separated node addresses, e.g. localhost:7001,localhost:7002",
    )
    p.add_argument("--virtual-nodes", type=int, default=100, help="Virtual nodes per physical node")
    return p.parse_args()


class ProxyRPCService:
    """Exposes KVGet / KVSet / KVDelete as XML-RPC methods."""

    def __init__(self, handler: ProxyHandler) -> None:
        self._handler = handler

    def KVGet(self, args: dict) -> str | None:
        return self._handler.get(args["key"])

    def KVSet(self, args: dict) -> bool:
        return self._handler.set(args["key"], args["value"], ttl=args.get("ttl"))

    def KVDelete(self, args: dict) -> bool:
        return self._handler.delete(args["key"])


def main() -> None:
    args = parse_args()
    nodes = [n.strip() for n in args.nodes.split(",") if n.strip()]

    ring = HashRing(nodes=nodes, virtual_nodes=args.virtual_nodes)
    handler = ProxyHandler(ring)
    service = ProxyRPCService(handler)

    server = SimpleXMLRPCServer(
        ("0.0.0.0", args.port), logRequests=False, allow_none=True
    )
    server.register_instance(service)

    log.info("Proxy listening on port %d, nodes=%s", args.port, nodes)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
        log.info("Proxy shut down")


if __name__ == "__main__":
    main()
