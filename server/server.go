package server

import (
	"fmt"
	"log"
	"net/http"
	"path"
	"regexp"
)

type Request struct {
	*http.Request
	RepoName string
	RepoPath string
}

type service struct {
	method  string
	pattern *regexp.Regexp
	handler func(srv string, w http.ResponseWriter, r *Request)
	rpc     string
}

type Server struct {
	config   Config
	services []service
	AuthFunc func(Credential, *Request) (allow bool, err error)
	//storage *storage.Storage
}

func NewServer(cfg Config) *Server {
	s := Server{config: cfg}
	s.services = []service{
		//Dumb Protocol
		{method: "GET", pattern: regexp.MustCompile("(.*?)/HEAD$"), handler: s.handleGetHead},
		{method: "GET", pattern: regexp.MustCompile("(.*?)/info/refs$"), handler: s.handleRefs},
		{method: "GET", pattern: regexp.MustCompile("(.*?)/objects/info/alternates$"), handler: s.handleAlts},
		{method: "GET", pattern: regexp.MustCompile("(.*?)/objects/info/http-alternates$"), handler: s.handleAlts},
		{method: "GET", pattern: regexp.MustCompile("(.*?)/objects/info/packs$"), handler: s.handleInfoPacks},
		{method: "GET", pattern: regexp.MustCompile("(.*?)/objects/[0-9a-f]{2}/[0-9a-f]{38,62}$"), handler: s.handleLooseObj},
		{method: "GET", pattern: regexp.MustCompile(`(.*?)/objects/pack/pack-[0-9a-f]{40,64}\.pack$`), handler: s.handlePackFile},
		{method: "GET", pattern: regexp.MustCompile(`(.*?)/objects/pack/pack-[0-9a-f]{40,64}\.idx$`), handler: s.handlePackIdx},
		//Smart Protocol
		{method: "GET", pattern: regexp.MustCompile("(.*?)/git-upload-pack$"), handler: s.handleUploadPack, rpc: "git-upload-pack"},
		{method: "GET", pattern: regexp.MustCompile("(.*?)/git-receive-pack$"), handler: s.handleReceivePack, rpc: "git-receive-pack"},
		{method: "GET", pattern: regexp.MustCompile("(.*?)/git-upload-archive$"), handler: s.handleUploadArchive, rpc: "git-upload-archive"},
	}

	return &s

}

func (s *Server) route(r *http.Request) (srv *service, path string) {
	for _, s := range s.services {
		if pathParts := s.pattern.FindStringSubmatch(r.URL.Path); pathParts != nil {
			if s.method == r.Method {
				path := pathParts[0]
				return &s, path
			}
		}
	}
	return nil, ""
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log.Println("request", r.Method+" "+r.Host+" "+r.URL.String())

	srv, repoPath := s.route(r)
	if srv == nil {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	repoNS, repoName := getNamespaceAndRepo(repoPath)
	if repoName == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	req := Request{
		Request:  r,
		RepoName: repoNS + repoName,
		RepoPath: path.Join(s.config.ReposDir, repoNS),
	}

	if s.config.Auth {
		if s.AuthFunc == nil {
			log.Println("ERROR[AUTH]: Auth is enabled without auth`s function!")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			w.Header()["WWW-Authenticate"] = []string{`Basic realm=""`}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		cred, err := getCredential(r)
		if err != nil {
			log.Println("Error[AUTH]: ", err)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		allow, err := s.AuthFunc(cred, &req)
		if !allow || err != nil {
			if err != nil {
				log.Println("WARNING[AUTH]:" + err.Error())
			}

			log.Println("auth", fmt.Errorf("rejected user %s", cred.Username))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}

	//TODO: Repo exists? If not, return 404

	srv.handler(srv.rpc, w, &req)
}
