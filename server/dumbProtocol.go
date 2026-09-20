package server

import "net/http"

func (s *Server) handleGetHead(srv string, w http.ResponseWriter, r *Request) {
	http.Error(w, "Not implemented!", http.StatusNotImplemented)
}

func (s *Server) handleRefs(srv string, w http.ResponseWriter, r *Request) {
	http.Error(w, "Not implemented!", http.StatusNotImplemented)
}

func (s *Server) handleAlts(srv string, w http.ResponseWriter, r *Request) {
	http.Error(w, "Not implemented!", http.StatusNotImplemented)
}

func (s *Server) handleInfoPacks(srv string, w http.ResponseWriter, r *Request) {
	http.Error(w, "Not implemented!", http.StatusNotImplemented)
}

func (s *Server) handleLooseObj(srv string, w http.ResponseWriter, r *Request) {
	http.Error(w, "Not implemented!", http.StatusNotImplemented)
}

func (s *Server) handlePackFile(srv string, w http.ResponseWriter, r *Request) {
	http.Error(w, "Not implemented!", http.StatusNotImplemented)
}

func (s *Server) handlePackIdx(srv string, w http.ResponseWriter, r *Request) {
	http.Error(w, "Not implemented!", http.StatusNotImplemented)
}
