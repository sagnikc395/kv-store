from enum import IntEnum


DEBUG_CM = 1


class CMState(IntEnum):
    Follower = 1
    Candidate = 2
    Leader = 3
    Dead = 4

    def __str__(self) -> str:
        match self:
            case CMState.Follower:
                return "Follower"
            case CMState.Candidate:
                return "Candidate"
            case CMState.Leader:
                return "Leader"
            case CMState.Dead:
                return "Dead"
            case _:
                return "unreachable"
