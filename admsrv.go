package main

import (
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strings"
)

// Define a struct for our v11 Server type
type Server struct {
	Name                         string
	SelfRegistered               bool
	Host                         string
	IPAddress                    NullableString
	IPv6Address                  NullableString
	PortNumber                   NullableInt
	ClientMessagePortNumber      NullableInt
	HTTPPortNumber               int
	IsLocal                      bool
	UsingSSL                     bool
	SSLCertificateID             NullableString
	SSLCertificateAuthority      NullableString
	SSLCertificateRevocationList NullableString
	ClientExportSSLSvrCert       bool
	ClientExportSSLSvrKeyID      NullableString
	AcceptingClients             bool
	LastUpdated                  string
	httpServer                   *http.Server `json:"-"`
}

type ServerResponse struct {
	ContextURL string `json:"@odata.context"`
	Server
}

type ServersResponse struct {
	ContextURL string   `json:"@odata.context"`
	Servers    []Server `json:"value"`
}

func NewServer() Server {
	server := Server{}
	return server
}

func NewServerResponse(server Server) ServerResponse {
	response := ServerResponse{ContextURL: "$metadata#Servers/$entity", Server: server}
	return response
}

func NewServersResponse(servers []Server) ServersResponse {
	response := ServersResponse{ContextURL: "$metadata#Servers", Servers: servers}
	return response
}

// Handler for requests for the Servers entity set
func serverCollectionResource(w http.ResponseWriter, r *http.Request) {
	// Only allow GET requests
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get the list of currently active servers
	servers := listServers()

	// Return the Servers collection as JSON
	serversResponse := NewServersResponse(servers)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(serversResponse)
}

// Handler for request for a single Server entity
func serverResource(w http.ResponseWriter, r *http.Request, name string) {
	// Only allow GET requests
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// Look up the server in list of currently active servers
	server := lookupServer(name)
	if server == nil {
		http.NotFound(w, r)
		return
	}

	// Return the specific item as JSON
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(NewServerResponse(*server))
}

// OData metadata document handler, returning either JSON/XML version
func metadataResource(w http.ResponseWriter, r *http.Request) {
	// Only allow GET requests
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check if by any chance the '$format' query parameter has been specified
	accept := r.URL.Query().Get("$format")
	if accept != "" {
		if accept != "application/json" && accept != "application/xml" {
			http.Error(w, "Content-Type specified in $format query parameter not supported", http.StatusBadRequest)
			return
		}
	} else {
		// $format not specified, check Accept header to decide the response format
		accept = r.Header.Get("Accept")
	}

	// XML is the default return JSON if explicitly allowed
	if strings.Contains(accept, "application/json") {
		// Read the XML version of the metadata document
		data, err := os.ReadFile("./metadata.json")
		if err != nil {
			http.Error(w, "Failed to read $metadata (JSON) file", http.StatusInternalServerError)
			return
		}
		// Write the response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	} else {
		// Read the XML version of the metadata document
		data, err := os.ReadFile("./metadata.xml")
		if err != nil {
			http.Error(w, "Failed to read $metadata (XML) file", http.StatusInternalServerError)
			return
		}
		// Write the response
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	}
}

// OData service document handler, only support JSON, we won't even check
func serviceDocumentResource(w http.ResponseWriter, r *http.Request) {
	// Only allow GET requests
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// Read the service document
	data, err := os.ReadFile("./service.json")
	if err != nil {
		http.Error(w, "Failed to read service document (JSON) file", http.StatusInternalServerError)
		return
	}
	// Write the response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

type admsrvRouter struct{}

// Regular expression to match "/Servers('{name}')" format
var serverPathRegex = regexp.MustCompile(`^Servers\(\'([^\/]+)\'\)$`)

func (t *admsrvRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Ensure the path starts with "/api/v1/"
	segments := strings.SplitN(r.URL.Path[1:], "/", 3) // Split into max 3 parts

	// Check if the first two fragments are the common prefix "api/v1"
	if len(segments) < 3 || segments[0] != "api" || segments[1] != "v1" {
		http.NotFound(w, r)
		return
	}
	path := segments[2]

	// Use regex to check if this is a request for a specific server
	matches := serverPathRegex.FindStringSubmatch(path)

	// Route the request

	if len(matches) == 2 {
		serverResource(w, r, matches[1])
	} else if path == "Servers" {
		serverCollectionResource(w, r)
	} else if path == "$metadata" {
		metadataResource(w, r)
	} else if path == "" {
		serviceDocumentResource(w, r)
	} else {
		http.NotFound(w, r)
	}
}
