import logging
from dataclasses import dataclass
from typing import Any

log = logging.getLogger(__name__)


@dataclass
class LogEntry:
    command: Any
    term: int

    def to_dict(self) -> dict:
        return {"command": self.command, "term": self.term}

    @classmethod
    def from_dict(cls, d: dict) -> "LogEntry":
        return cls(command=d["command"], term=d["term"])
