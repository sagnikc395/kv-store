import logging
import os
import queue
import random
import threading
import time
from xmlrpc.client import ServerProxy
from xmlrpc.server import SimpleXMLRPCServer

from .raft import ConsensusModule
from .types import (
    AppendEntriesArgs,
    AppendEntriesReply,
    RequestVoteArgs,
    RequestVoteReply,
)

logging.basicConfig(level=logging.DEBUG, format="%(message)s")


class Server:
    def __init__(self, server_id: int, peer_ids: list[int], ready: threading.Event):
        self.server_id = server_id
        self.peer_ids = peer_ids
        self.peer_clients: dict[int, ServerProxy | None] = {}
        self.ready = ready

        self.commit_chan: queue.Queue = queue.Queue()
        self.cm: ConsensusModule | None = None
        self.rpc_server: SimpleXMLRPCServer | None = None

        self.mu = threading.Lock()

    def serve(self) -> None:
        with self.mu:
            # Bind to an OS-assigned port so tests can run multiple servers
            self.rpc_server = SimpleXMLRPCServer(
                ("127.0.0.1", 0), logRequests=False, allow_none=True
            )
            self.cm = ConsensusModule(
                self.server_id, self.peer_ids, self, self.ready, self.commit_chan
            )
            self.rpc_server.register_instance(RPCProxy(self.cm))

        t = threading.Thread(target=self.rpc_server.serve_forever, daemon=True)
        t.start()

    # ── lifecycle ──────────────────────────────

    def shutdown(self) -> None:
        self.cm.stop()
        self.rpc_server.shutdown()

    # ── peer management ───────────────────────

    def get_listen_addr(self) -> tuple[str, int]:
        with self.mu:
            return self.rpc_server.server_address

    def connect_to_peer(self, peer_id: int, addr: tuple[str, int]) -> None:
        with self.mu:
            if self.peer_clients.get(peer_id) is None:
                self.peer_clients[peer_id] = ServerProxy(
                    f"http://{addr[0]}:{addr[1]}/", allow_none=True
                )

    def disconnect_peer(self, peer_id: int) -> None:
        with self.mu:
            if self.peer_clients.get(peer_id) is not None:
                self.peer_clients[peer_id] = None

    def disconnect_all(self) -> None:
        with self.mu:
            for pid in self.peer_clients:
                self.peer_clients[pid] = None

    # ── RPC call ─────────────────────────────

    def call(self, peer_id: int, method: str, args: dict) -> dict:
        """Forward an RPC call to a peer."""
        with self.mu:
            peer = self.peer_clients.get(peer_id)

        if peer is None:
            raise ConnectionError(f"call client {peer_id} after it's closed")

        rpc_fn = getattr(peer, method)
        return rpc_fn(args)


class RPCProxy:
    """
    Thin pass-through proxy for ConsensusModule RPCs.
    Simulates unreliable network when RAFT_UNRELIABLE_RPC is set.
    """

    def __init__(self, cm: ConsensusModule) -> None:
        self.cm = cm

    def _maybe_drop_or_delay(self, method_name: str) -> bool:
        dice = random.randint(0, 9)
        if dice == 9:
            self.cm.dlog("drop %s", method_name)
            return True
        if dice == 8:
            self.cm.dlog("delay %s", method_name)
            time.sleep(0.075)
        return False

    def RequestVote(self, args: dict) -> dict:
        if os.environ.get("RAFT_UNRELIABLE_RPC"):
            if self._maybe_drop_or_delay("RequestVote"):
                raise RuntimeError("RPC dropped")
        else:
            time.sleep(random.randint(1, 5) / 1000)

        reply = RequestVoteReply()
        self.cm.request_vote(RequestVoteArgs(**args), reply)
        return reply.__dict__

    def AppendEntries(self, args: dict) -> dict:
        if os.environ.get("RAFT_UNRELIABLE_RPC"):
            if self._maybe_drop_or_delay("AppendEntries"):
                raise RuntimeError("RPC dropped")
        else:
            time.sleep(random.randint(1, 5) / 1000)

        reply = AppendEntriesReply()
        self.cm.append_entries(AppendEntriesArgs(**args), reply)
        return reply.__dict__
