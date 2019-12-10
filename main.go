package main

import (
	"crypto/tls"
	"flag"
	"io"
	"log"
	"net"
	"time"
)

func main() {
	localAddr := flag.String("l", ":6360", "local address")
	remoteAddr := flag.String("r", "remote_server.test:636", "remote address")
	unwrapTLS := flag.Bool("tls", false, "remote connection with TLS exposed unencrypted locally")
	flag.Parse()
	log.Printf("proxying from %v to %v", *localAddr, *remoteAddr)
	laddr, err := net.ResolveTCPAddr("tcp", *localAddr)
	if err != nil {
		log.Fatalf("failed to resolve local address: %s", err)
	}
	raddr, err := net.ResolveTCPAddr("tcp", *remoteAddr)
	if err != nil {
		log.Fatalf("failed to resolve remote address: %s", err)
	}
	listener, err := net.ListenTCP("tcp", laddr)
	if err != nil {
		log.Fatalf("failed to open local port to listen: %s", err)
	}

	var connId uint64
	for {
		conn, err := listener.AcceptTCP()
		if err != nil {
			log.Printf("failed to accept connection '%s'", err)
			continue
		}
		connId++
		var p *Proxy
		if *unwrapTLS {
			p = NewTLSUnwrapped(connId, conn, laddr, raddr, *remoteAddr)
		} else {
			p = New(connId, conn, laddr, raddr)
		}
		go p.Start()
	}
}

type Proxy struct {
	id                    uint64
	sentBytes             uint64
	receivedBytes         uint64
	localAddr, remoteAddr *net.TCPAddr
	localConn, remoteConn io.ReadWriteCloser
	erred                 bool
	stopSignal            chan bool
	tlsUnwrap             bool
	tlsAddress            string
}

func New(id uint64, lconn *net.TCPConn, laddr, raddr *net.TCPAddr) *Proxy {
	return &Proxy{
		id:         id,
		localConn:  lconn,
		localAddr:  laddr,
		remoteAddr: raddr,
		erred:      false,
		stopSignal: make(chan bool),
	}
}

func NewTLSUnwrapped(id uint64, lconn *net.TCPConn, laddr, raddr *net.TCPAddr, addr string) *Proxy {
	p := New(id, lconn, laddr, raddr)
	p.tlsUnwrap = true
	p.tlsAddress = addr
	return p
}

func (p *Proxy) Start() {
	defer close(p.localConn)
	var err error
	//connect to remote
	if p.tlsUnwrap {
		p.remoteConn, err = tls.Dial("tcp", p.tlsAddress, nil)
	} else {
		p.remoteConn, err = net.DialTCP("tcp", nil, p.remoteAddr)
	}
	if err != nil {
		log.Printf("[%d] remote connection failed: %s", p.id, err)
		return
	}
	defer close(p.remoteConn)

	//display both ends
	log.Printf("[%d] opened %s > %s", p.id, p.localAddr.String(), p.remoteAddr.String())
	//bidirectional copy
	go p.pipe(p.localConn, p.remoteConn)
	go p.pipe(p.remoteConn, p.localConn)

	done := make(chan bool)
	go p.stats(done)

	<-p.stopSignal
	done <- true
	log.Printf("[%d] closed (%d bytes sent, %d bytes recieved)", p.id, p.sentBytes, p.receivedBytes)
}

func (p *Proxy) stats(done chan bool) {
	ticker := time.NewTicker(time.Second * 30)

	var prevSent uint64
	var prevReceived uint64
	for {
		select {
		case <-done:
			ticker.Stop()
			return
		case <-ticker.C:
			if prevSent != p.sentBytes || prevReceived != p.receivedBytes {
				// it's fine that we have a read race conditions here, doesnt matter if we are super up-to-date on the informational numbers
				log.Printf("[%d] %d bytes sent, %d bytes recieved", p.id, p.sentBytes, p.receivedBytes)
				prevSent, prevReceived = p.sentBytes, p.receivedBytes
			}
		}
	}
}

func (p *Proxy) err(s string, err error) {
	if p.erred {
		return // prevent read error to cause a fake write error
	}
	if err != io.EOF {
		log.Printf("[%d] "+s, p.id, err)
	}
	p.stopSignal <- true
	p.erred = true
}

func (p *Proxy) pipe(src, dst io.ReadWriter) {
	isLocal := src == p.localConn
	//directional copy (64k buffer)
	buff := make([]byte, 0xffff)

	for {
		n, err := src.Read(buff)
		if err != nil {
			p.err("read failed '%s'\n", err)
			return
		}
		n, err = dst.Write(buff[:n])
		if err != nil {
			p.err("write failed '%s'\n", err)
			return
		}
		if isLocal {
			p.sentBytes += uint64(n)
		} else {
			p.receivedBytes += uint64(n)
		}
	}
}

func close(closer io.Closer) {
	if err := closer.Close(); err != nil {
		log.Printf("error while closing: %s\n", err)
	}
}
