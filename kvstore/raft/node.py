## implements the consensus module , the heart of the Raft algorithm

from .server import Server


class ConsensusModule:
    # server id of this consensus module
    server_id: int
    # peerids will list the ids of our peers in the cluster
    peer_ids: list[int]

    # server is the server containing this consensus module. it is used to issue RPC calls to peers
    server: Server

    
