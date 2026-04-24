import logging
import os
import queue
import random
import socket
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
        self.quit = threading.Event()

        self.commit_chan: queue.Queue = queue.Queue()
        self.cm: ConsensusModule | None = None
        self.rpc_server: SimpleXMLRPCServer | None = None
        self.rpc_proxy: "RPCProxy | None" = None
        self.listener: socket.socket | None = None

        self.mu = threading.Lock()
        self._active_threads: list[threading.Thread] = []

    def serve(self) -> None:
        with self.mu:
            self.cm = ConsensusModule(
                self.server_id, self.peer_ids, self, self.ready, self.commit_chan
            )
            self.rpc_proxy = RPCProxy(self.cm)

            self.listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
            self.listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            self.listener.bind(("", 0))
            self.listener.listen(5)
            addr = self.listener.getsockname()
            logging.debug("[%s] listening at %s:%s", self.server_id, addr[0], addr[1])

            self.rpc_server = SimpleXMLRPCServer(
                addr, logRequests=False, allow_none=True, bind_and_activate=False
            )
            self.rpc_server.register_instance(self.rpc_proxy)

        t = threading.Thread(target=self._accept_loop, daemon=True)
        self._active_threads.append(t)
        t.start()

    def _accept_loop(self) -> None:
        while True:
            try:
                self.listener.settimeout(1.0)
                conn, _ = self.listener.accept()
            except TimeoutError:
                if self.quit.is_set():
                    return
                continue
            except OSError:
                if self.quit.is_set():
                    return
                logging.fatal("accept error")
                raise

            t = threading.Thread(target=self._serve_conn, args=(conn,), daemon=True)
            self._active_threads.append(t)
            t.start()

    def _serve_conn(self, conn: socket.socket) -> None:
        try:
            self.rpc_server.socket = conn
            self.rpc_server._handle_request_noblock()
        finally:
            conn.close()

    # ── lifecycle ──────────────────────────────

    def shutdown(self) -> None:
        self.cm.stop()
        self.quit.set()
        self.listener.close()
        for t in self._active_threads:
            t.join(timeout=2.0)

    # ── peer management ───────────────────────

    def get_listen_addr(self) -> tuple[str, int]:
        with self.mu:
            return self.listener.getsockname()

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
        with self.mu:
            peer = self.peer_clients.get(peer_id)

        if peer is None:
            raise ConnectionError(f"call client {peer_id} after it's closed")

        rpc_fn = getattr(peer, method)
        return rpc_fn(args)


class RPCProxy:
    """
    Trivial pass-through proxy for ConsensusModule's RPC methods.
    Optionally simulates unreliable network via RAFT_UNRELIABLE_RPC.
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
                raise RuntimeError("RPC failed")
        else:
            time.sleep(random.randint(1, 5) / 1000)

        reply = RequestVoteReply()
        self.cm.request_vote(RequestVoteArgs(**args), reply)
        return reply.__dict__

    def AppendEntries(self, args: dict) -> dict:
        if os.environ.get("RAFT_UNRELIABLE_RPC"):
            if self._maybe_drop_or_delay("AppendEntries"):
                raise RuntimeError("RPC failed")
        else:
            time.sleep(random.randint(1, 5) / 1000)

        reply = AppendEntriesReply()
        self.cm.append_entries(AppendEntriesArgs(**args), reply)
        return reply.__dict__
