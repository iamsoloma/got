package server

import "net/http"

func (s *Server) handleUploadPack(srv string, w http.ResponseWriter, r *Request) {
	http.Error(w, "Not implemented!", http.StatusNotImplemented)
}

func (s *Server) handleReceivePack(srv string, w http.ResponseWriter, r *Request) {
	http.Error(w, "Not implemented!", http.StatusNotImplemented)
}

func (s *Server) handleUploadArchive(srv string, w http.ResponseWriter, r *Request) {
	http.Error(w, "Not implemented!", http.StatusNotImplemented)
}
