import logging

log = logging.getLogger(__name__)


class LogEntry:
    def __init__(self, command, term: int) -> None:
        self.command = command
        self.term = term
