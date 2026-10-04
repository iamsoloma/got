package server

import (
	"got/git"
	"log"
	"net/http"
)

func (s *Server) handleGetHead(srv string, w http.ResponseWriter, r *Request) {
	if s.storage == nil {
		http.Error(w, "storage not initialized", http.StatusInternalServerError)
		return
	}

	//fmt.Printf("Opening repository: %s\n", r.RepoName)

	repo, err := git.Open(*s.storage, s.config.ReposDir+"/"+r.RepoName)
	if err != nil {
		http.Error(w, "Error opening repository", http.StatusInternalServerError)
		log.Printf("Error opening repository: %v", err)
		return
	}
	head, err := repo.Storage.GetHEAD()
	if err != nil {
		http.Error(w, "Error getting HEAD", http.StatusInternalServerError)
		log.Printf("Error getting HEAD: %v", err)
		return
	}

	w.Header().Set("Content-Type", "text/plain;charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")

	w.Write([]byte(head))
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
