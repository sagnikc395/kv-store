import socket
import logging
import threading

from .raft import ConsensusModule
from xmlrpc.server import SimpleXMLRPCServer
from xmlrpc.client import ServerProxy
import logging

logging.basicConfig(level=logging.DEBUG, format="%(message)s")


class Server:
    def __init__(self, server_id: int, peer_ids: list[int], ready: threading.Event):
        self.server_id = server_id
        self.peer_ids = peer_ids
        self.peer_clients: dict[int, ServerProxy] = {}
        self.ready = ready
        self.quit = threading.Event()

        self.cm = None
        self.rpc_server = None
        self.rpc_proxy = None
        self.listener = None

        self.mu = threading.Lock()
        # using a Barrirer as a Waitgroup via active thread count
        self.wg = threading.Barrier(1)
        self._active_threads: list[threading.Thread] = []

    def serve(self):
        with self.mu:
            self.cm = ConsensusModule(self.server_id, self.peer_ids, self, self.ready)

            self.rpc_proxy = RPCProxy(self.cm)
            # Bind to a random available port (equivalent to ":0")
            self.listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
            self.listener.bind(("", 0))
            self.listener.listen(5)
            addr = self.listener.getsockname()
            logging.debug(f"[{self.server_id}] listening at {addr[0]}:{addr[1]}")

            self.rpc_server = SimpleXMLRPCServer(
                addr, logRequests=False, allow_none=True, bind_and_activate=False
            )
            self.rpc_server.register_instance(self.rpc_proxy)

        def accept_loop():
            while True:
                try:
                    self.listener.settimeout(1.0)
                    conn, _ = self.listener.accept()
                except socket.timeout:
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

        t = threading.Thread(target=accept_loop, daemon=True)
        self._active_threads.append(t)
        t.start()

    def _serve_conn(self, conn):
        try:
            self.rpc_server.socket = conn
            self.rpc_server._handle_request_noblock()
        finally:
            conn.close()

    def stop(self):
        self.quit.set()
        self.listener.close()
        for t in self._active_threads:
            t.join()

class RPCProxy:
    # a trivial pass-thru proxy type for CM RPC methods 
    
    def __