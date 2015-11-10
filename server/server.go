package server

import (
	"net"
	"net/http"

	"github.com/gorilla/context"

	"github.com/getlantern/golog"

	"github.com/getlantern/http-proxy/listeners"
)

var (
	testingLocal = false
	log          = golog.LoggerFor("server")
)

type listenerGenerator func(net.Listener) net.Listener

type Server struct {
	Addr net.Addr
	Tls  bool

	handler            http.Handler
	listener           net.Listener
	httpServer         http.Server
	listenerGenerators []listenerGenerator
}

func NewServer(handler http.Handler) *Server {
	server := &Server{
		handler: handler,
	}

	return server
}

func (s *Server) AddListenerWrappers(listenerGens ...listenerGenerator) {
	for _, g := range listenerGens {
		s.listenerGenerators = append(s.listenerGenerators, g)
	}
}

func (s *Server) ServeHTTP(addr string, chListenOn *chan string) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.Tls = false
	log.Debugf("Listen http on %s", addr)
	return s.doServe(listener, chListenOn)
}

func (s *Server) ServeHTTPS(addr, keyfile, certfile string, chListenOn *chan string) error {
	listener, err := listenTLS(addr, keyfile, certfile)
	if err != nil {
		return err
	}
	s.Tls = true
	log.Debugf("Listen https on %s", addr)
	return s.doServe(listener, chListenOn)
}

func (s *Server) doServe(listener net.Listener, chListenOn *chan string) error {
	cb := connBag{m: make(map[string]net.Conn)}

	proxy := http.HandlerFunc(
		func(w http.ResponseWriter, req *http.Request) {
			c := cb.Withdraw(req.RemoteAddr)
			context.Set(req, "conn", c)
			s.handler.ServeHTTP(w, req)
		})

	s.httpServer = http.Server{Handler: proxy,
		ConnState: func(c net.Conn, state http.ConnState) {
			awareconn, ok := c.(listeners.StateAware)
			if ok {
				awareconn.OnState(state)
			}

			switch state {
			case http.StateActive:
				cb.Put(c)
			case http.StateClosed:
				// When go server encounters abnormal request, it
				// will transit to StateClosed directly without
				// the handler being invoked, hence the connection
				// will not be withdrawed. Purge it in such case.
				cb.Purge(c.RemoteAddr().String())
			}
		},
	}

	firstListener := &listener
	for _, li := range s.listenerGenerators {
		newlis := li(*firstListener)
		firstListener = &newlis
	}
	s.listener = *firstListener

	s.Addr = s.listener.Addr()
	addrStr := s.Addr.String()
	s.httpServer.Addr = addrStr
	if chListenOn != nil {
		*chListenOn <- addrStr
	}

	return s.httpServer.Serve(*firstListener)
}