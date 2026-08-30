package server

import (
	"log/slog"

	"wsproxy/internal/allow"
)

type Server struct {
	HTTPAddr    string
	SSHAddr     string
	AgentToken  string
	AccessToken string
	HostKeyPath string
	Hub         *Hub
}

func (s *Server) SetAllow(a allow.Sets) {
	s.Hub.SetAllow(a)
}

func New(httpAddr, sshAddr, agentToken, accessToken, hostKeyPath string) *Server {
	return &Server{
		HTTPAddr:    httpAddr,
		SSHAddr:     sshAddr,
		AgentToken:  agentToken,
		AccessToken: accessToken,
		HostKeyPath: hostKeyPath,
		Hub:         NewHub(accessToken),
	}
}

func (s *Server) Run() error {
	errCh := make(chan error, 2)
	go func() { errCh <- s.startHTTP() }()
	go func() { errCh <- s.startSSH() }()
	err := <-errCh
	slog.Error("server stopped", "err", err)
	return err
}
