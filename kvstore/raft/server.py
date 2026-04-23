import json
import socket
import inspect
from threading import Thread


class Server:
    def __init__(self, host: str = "0.0.0.0", port: int = 8080) -> None:
        self.host = host
        self.port = port
        self.address = (host, port)
        self._methods = {}
