package core

import (
	"context"
	"io"
	"net"
	"net/http"
	gopath "path"
	"strings"
	"sync"
	"time"

	"github.com/aler9/rtsp-simple-server/internal/logger"
)

type hlsServerParent interface {
	Log(logger.Level, string, ...interface{})
}

type hlsServer struct {
	hlsAlwaysRemux     bool
	hlsSegmentCount    int
	hlsSegmentDuration time.Duration
	hlsAllowOrigin     string
	readBufferCount    int
	pathManager        *pathManager
	parent             hlsServerParent

	ctx       context.Context
	ctxCancel func()
	wg        sync.WaitGroup
	ln        net.Listener
	remuxers  map[string]*hlsRemuxer

	// in
	pathSourceReady chan *path
	request         chan hlsRemuxerRequest
	remuxerClose    chan *hlsRemuxer
}

func newHLSServer(
	parentCtx context.Context,
	address string,
	hlsAlwaysRemux bool,
	hlsSegmentCount int,
	hlsSegmentDuration time.Duration,
	hlsAllowOrigin string,
	readBufferCount int,
	pathManager *pathManager,
	parent hlsServerParent,
) (*hlsServer, error) {
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}

	ctx, ctxCancel := context.WithCancel(parentCtx)

	s := &hlsServer{
		hlsAlwaysRemux:     hlsAlwaysRemux,
		hlsSegmentCount:    hlsSegmentCount,
		hlsSegmentDuration: hlsSegmentDuration,
		hlsAllowOrigin:     hlsAllowOrigin,
		readBufferCount:    readBufferCount,
		pathManager:        pathManager,
		parent:             parent,
		ctx:                ctx,
		ctxCancel:          ctxCancel,
		ln:                 ln,
		remuxers:           make(map[string]*hlsRemuxer),
		pathSourceReady:    make(chan *path),
		request:            make(chan hlsRemuxerRequest),
		remuxerClose:       make(chan *hlsRemuxer),
	}

	s.Log(logger.Info, "listener opened on "+address)

	s.pathManager.OnHLSServerSet(s)

	s.wg.Add(1)
	go s.run()

	return s, nil
}

// Log is the main logging function.
func (s *hlsServer) Log(level logger.Level, format string, args ...interface{}) {
	s.parent.Log(level, "[HLS] "+format, append([]interface{}{}, args...)...)
}

func (s *hlsServer) close() {
	s.ctxCancel()
	s.wg.Wait()
	s.Log(logger.Info, "closed")
}

func (s *hlsServer) run() {
	defer s.wg.Done()

	hs := &http.Server{Handler: s}
	go hs.Serve(s.ln)

outer:
	for {
		select {
		case pa := <-s.pathSourceReady:
			if s.hlsAlwaysRemux {
				s.findOrCreateRemuxer(pa.Name())
			}

		case req := <-s.request:
			r := s.findOrCreateRemuxer(req.Dir)
			r.OnRequest(req)

		case c := <-s.remuxerClose:
			if c2, ok := s.remuxers[c.PathName()]; !ok || c2 != c {
				continue
			}
			delete(s.remuxers, c.PathName())

		case <-s.ctx.Done():
			break outer
		}
	}

	s.ctxCancel()

	hs.Shutdown(context.Background())

	s.pathManager.OnHLSServerSet(nil)
}

// ServeHTTP implements http.Handler.
func (s *hlsServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.Log(logger.Info, "[conn %v] %s %s", r.RemoteAddr, r.Method, r.URL.Path)

	// remove leading prefix
	pa := r.URL.Path[1:]

	w.Header().Add("Access-Control-Allow-Origin", s.hlsAllowOrigin)
	w.Header().Add("Access-Control-Allow-Credentials", "true")

	switch r.Method {
	case http.MethodGet:

	case http.MethodOptions:
		w.Header().Add("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Add("Access-Control-Allow-Headers", r.Header.Get("Access-Control-Request-Headers"))
		w.WriteHeader(http.StatusOK)
		return

	default:
		w.WriteHeader(http.StatusNotFound)
		return
	}

	switch pa {
	case "", "favicon.ico":
		w.WriteHeader(http.StatusNotFound)
		return
	}

	dir, fname := func() (string, string) {
		if strings.HasSuffix(pa, ".ts") || strings.HasSuffix(pa, ".m3u8") {
			return gopath.Dir(pa), gopath.Base(pa)
		}
		return pa, ""
	}()

	if fname == "" && !strings.HasSuffix(dir, "/") {
		w.Header().Add("Location", "/"+dir+"/")
		w.WriteHeader(http.StatusMovedPermanently)
		return
	}

	dir = strings.TrimSuffix(dir, "/")

	cres := make(chan io.Reader)
	hreq := hlsRemuxerRequest{
		Dir:  dir,
		File: fname,
		Req:  r,
		W:    w,
		Res:  cres,
	}

	select {
	case s.request <- hreq:
		res := <-cres

		if res != nil {
			buf := make([]byte, 4096)
			for {
				n, err := res.Read(buf)
				if err != nil {
					return
				}

				_, err = w.Write(buf[:n])
				if err != nil {
					return
				}

				w.(http.Flusher).Flush()
			}
		}

	case <-s.ctx.Done():
	}
}

func (s *hlsServer) findOrCreateRemuxer(pathName string) *hlsRemuxer {
	r, ok := s.remuxers[pathName]
	if !ok {
		r = newHLSRemuxer(
			s.ctx,
			s.hlsAlwaysRemux,
			s.hlsSegmentCount,
			s.hlsSegmentDuration,
			s.readBufferCount,
			&s.wg,
			pathName,
			s.pathManager,
			s)
		s.remuxers[pathName] = r
	}
	return r
}

// OnRemuxerClose is called by hlsRemuxer.
func (s *hlsServer) OnRemuxerClose(c *hlsRemuxer) {
	select {
	case s.remuxerClose <- c:
	case <-s.ctx.Done():
	}
}

// OnPathSourceReady is called by core.
func (s *hlsServer) OnPathSourceReady(pa *path) {
	select {
	case s.pathSourceReady <- pa:
	case <-s.ctx.Done():
	}
}
