package raft

import (
	"errors"
	"math/rand"
	"net"
	"net/rpc"
	"os"
	"sync"
	"time"
)

type Server struct {
	ID      int
	PeerIDs []int
	Ready   <-chan struct{}

	CommitChan chan LogEntry
	CM         *ConsensusModule

	mu          sync.Mutex
	rpcServer   *rpc.Server
	listener    net.Listener
	peerClients map[int]*rpc.Client
}

func NewServer(id int, peerIDs []int, ready <-chan struct{}) *Server {
	return &Server{
		ID:          id,
		PeerIDs:     append([]int(nil), peerIDs...),
		Ready:       ready,
		CommitChan:  make(chan LogEntry, 128),
		peerClients: make(map[int]*rpc.Client),
	}
}

func (s *Server) Serve() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}

	s.listener = l
	s.rpcServer = rpc.NewServer()
	s.CM = NewConsensusModule(s.ID, s.PeerIDs, s, s.Ready, s.CommitChan)
	if err := s.rpcServer.RegisterName("Raft", &rpcProxy{cm: s.CM}); err != nil {
		_ = l.Close()
		return err
	}

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go s.rpcServer.ServeConn(conn)
		}
	}()
	return nil
}

func (s *Server) RegisterName(name string, receiver any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rpcServer == nil {
		return errors.New("server is not serving")
	}
	return s.rpcServer.RegisterName(name, receiver)
}

func (s *Server) Shutdown() {
	s.mu.Lock()
	cm := s.CM
	listener := s.listener
	clients := s.peerClients
	s.peerClients = make(map[int]*rpc.Client)
	s.mu.Unlock()

	if cm != nil {
		cm.Stop()
	}
	for _, client := range clients {
		if client != nil {
			_ = client.Close()
		}
	}
	if listener != nil {
		_ = listener.Close()
	}
}

func (s *Server) ListenAddr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

func (s *Server) ConnectToPeer(peerID int, addr string) error {
	client, err := rpc.Dial("tcp", addr)
	if err != nil {
		return err
	}

	s.mu.Lock()
	old := s.peerClients[peerID]
	s.peerClients[peerID] = client
	s.mu.Unlock()

	if old != nil {
		_ = old.Close()
	}
	return nil
}

func (s *Server) DisconnectPeer(peerID int) {
	s.mu.Lock()
	client := s.peerClients[peerID]
	s.peerClients[peerID] = nil
	s.mu.Unlock()

	if client != nil {
		_ = client.Close()
	}
}

func (s *Server) DisconnectAll() {
	s.mu.Lock()
	ids := make([]int, 0, len(s.peerClients))
	for id := range s.peerClients {
		ids = append(ids, id)
	}
	s.mu.Unlock()

	for _, id := range ids {
		s.DisconnectPeer(id)
	}
}

func (s *Server) Call(peerID int, method string, args any, reply any) error {
	s.mu.Lock()
	client := s.peerClients[peerID]
	s.mu.Unlock()

	if client == nil {
		return errors.New("peer is disconnected")
	}
	return client.Call("Raft."+method, args, reply)
}

type rpcProxy struct {
	cm *ConsensusModule
}

func (p *rpcProxy) RequestVote(args RequestVoteArgs, reply *RequestVoteReply) error {
	if os.Getenv("RAFT_UNRELIABLE_RPC") != "" {
		if maybeDropOrDelay() {
			return errors.New("rpc dropped")
		}
	} else {
		time.Sleep(time.Duration(rand.Intn(5)+1) * time.Millisecond)
	}
	p.cm.RequestVote(args, reply)
	return nil
}

func (p *rpcProxy) AppendEntries(args AppendEntriesArgs, reply *AppendEntriesReply) error {
	if os.Getenv("RAFT_UNRELIABLE_RPC") != "" {
		if maybeDropOrDelay() {
			return errors.New("rpc dropped")
		}
	} else {
		time.Sleep(time.Duration(rand.Intn(5)+1) * time.Millisecond)
	}
	p.cm.AppendEntries(args, reply)
	return nil
}

func maybeDropOrDelay() bool {
	dice := rand.Intn(10)
	if dice == 9 {
		return true
	}
	if dice == 8 {
		time.Sleep(75 * time.Millisecond)
	}
	return false
}
