package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/spf13/viper"
	"go.uber.org/zap"
)

var (
	activeServersByName     = map[string]Server{}
	activeServersByPort     = map[int]string{}
	dictPortByServer        = map[string]int{}
	dictServerByPort        = map[int]string{}
	portLast            int = 0

	mu sync.Mutex
	wg sync.WaitGroup
)

// File in which we persist our server to port map
const portMapFilePath = "servers.json"

func savePortMapToFile() {
	mu.Lock()
	defer mu.Unlock()

	// Convert map to JSON
	jsonPortMap, err := json.MarshalIndent(dictPortByServer, "", "  ")
	if err != nil {
		logger.Error("Error marshalling port map to JSON", zap.Error(err))
		return
	}

	// Write JSON to file
	err = os.WriteFile(portMapFilePath, jsonPortMap, 0644)
	if err != nil {
		logger.Error("Error writing port map to file", zap.Error(err))
	}
}

func loadPortMapFromFile() {
	mu.Lock()
	defer mu.Unlock()

	// Check if file exists
	if _, err := os.Stat(portMapFilePath); os.IsNotExist(err) {
		return
	}

	// Read file content
	jsonPortMap, err := os.ReadFile(portMapFilePath)
	if err != nil {
		logger.Error("Error reading port map from file", zap.Error(err))
		return
	}

	// Parse JSON back to map
	var portMap map[string]int
	err = json.Unmarshal(jsonPortMap, &portMap)
	if err != nil {
		logger.Error("Error unmarshalling port map JSON", zap.Error(err))
		return
	}

	// Restore map
	dictPortByServer = make(map[string]int)
	dictServerByPort = make(map[int]string)
	for server, port := range portMap {
		dictPortByServer[server] = port
		dictServerByPort[port] = server
	}
}

func assignPort(name string) int {
	portMin := viper.GetInt("servers.port-range.min")
	portMax := viper.GetInt("servers.port-range.max")

	// Check if this server has had a port assigned to it that is:
	// - not currently being used
	// - and still in bounds of the port range
	if port, exists := dictPortByServer[name]; exists {
		if port >= portMin && port <= portMax {
			if _, exists := activeServersByPort[port]; !exists {
				// Reuse this port
				return port
			}
		} else {
			// Assigned port is outside the bounds of the [current] port range
			if server, exists := dictServerByPort[port]; exists && server == name {
				delete(dictServerByPort, port)
			}
			delete(dictPortByServer, name)
		}
	}

	// Bounds check the last assigned port as configuration might have changed
	if portLast < portMin || portLast > portMax {
		portLast = portMin - 1
		for _, port := range dictPortByServer {
			if port > portLast {
				if port > portMax {
					portLast = portMax
					break
				} else {
					portLast = port
				}
			}
		}
	}

	// Room left in the port range?
	if portLast < portMax {
		portLast++
		dictPortByServer[name] = portLast
		dictServerByPort[portLast] = name
		return portLast
	}

	// Must reuse the first port in the range that's not used
	for port := portMin; port <= portMax; port++ {
		if _, exists := activeServersByPort[port]; !exists {
			if server, exists := dictServerByPort[port]; exists {
				delete(dictPortByServer, server)
			}
			dictPortByServer[name] = port
			dictServerByPort[port] = name
			return port
		}
	}

	// No unused ports left in the port-range
	return 0
}

func startReverseProxy(server *Server) {
	// Map with the variables used to execute the template
	data := map[string]interface{}{
		"database": server.Name,
	}

	// Parse the template
	databaseUrlTemplate, err := template.New("datbaseUrl").Parse(viper.GetString("tm1-v12.database-url-template$"))
	if err != nil {
		logger.Error("Unable to start proxy, database URL template parsing failed", zap.Error(err), zap.String("tm1-v12.database-url-template", viper.GetString("tm1-v12.database-url-template")))
		return
	}

	// Execute the template with the data
	var target bytes.Buffer
	err = databaseUrlTemplate.Execute(&target, data)
	if err != nil {
		logger.Error("Unable to start proxy, failed to execute database URL template", zap.Error(err), zap.String("tm1-v12.database-url-template", viper.GetString("tm1-v12.database-url-template")))
		return
	}
	targetURL, err := url.Parse(target.String())
	if err != nil {
		logger.Error("Unable to start proxy, database URL template rendered an invalid URL", zap.Error(err), zap.String("tm1-v12.database-url-template", viper.GetString("tm1-v12.database-url-template")))
		return
	}

	// Start a new reverse proxy for targeting the targetURL
	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	// Modify the request before it is forwarded
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		// Validate that the URL starts with /api/v1
		if !strings.HasPrefix(req.URL.Path, "/api/v1/") {
			return
		}

		// Rewrite the URL to include the databases path segments
		req.URL.Path = req.URL.Path[7:]

		// call the original director to preserve all other request modifications
		originalDirector(req)
	}

	// Define a custom error handler to handle validation failures
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/") {
			http.Error(w, "Bad Request: Invalid Service Root URL", http.StatusBadRequest)
		}
	}

	// Now that we have initiated a reverse proxy handler for this database, start listening to the port associated to it
	server.httpServer = &http.Server{
		Addr:      ":" + strconv.Itoa(server.HTTPPortNumber),
		Handler:   proxy,
		TLSConfig: &tls.Config{},
	}

	logger.Info("Starting server proxy", zap.String("server", server.Name), zap.Int("port", server.HTTPPortNumber), zap.String("redirect-url", target.String()))

	// Increment the WaitGroup before starting the server goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()

		// Using SSL?
		if server.UsingSSL {
			if err := server.httpServer.ListenAndServeTLS(viper.GetString("servers.cert-file"), viper.GetString("servers.key-file")); err != http.ErrServerClosed {
				logger.Error("Proxy, using SSL, failed to start", zap.Error(err), zap.String("server", server.Name), zap.Int("port", server.HTTPPortNumber), zap.String("servers.cert-file", viper.GetString("servers.cert-file")), zap.String("servers.key-file", viper.GetString("servers.key-file")))
			}
		} else {
			if err := server.httpServer.ListenAndServe(); err != http.ErrServerClosed {
				logger.Error("Proxy failed to start", zap.Error(err), zap.String("server", server.Name), zap.Int("port", server.HTTPPortNumber))
			}
		}
	}()
}

func upsertServer(database *Database) {
	acceptsClients := func() bool {
		for _, replica := range database.ActiveReplicas {
			if replica.State == "ready" {
				return true
			}
		}
		return false
	}()

	// Check if we have a server representing this database already
	server, exists := activeServersByName[database.Name]
	if !exists {
		server = NewServer()
		server.Name = database.Name
		server.Host = viper.GetString("servers.host-name")
		server.IPAddress = NullableString(viper.GetString("servers.ip-v4-address$"))
		server.IPv6Address = NullableString(viper.GetString("servers.ip-v6-address$"))
		server.HTTPPortNumber = assignPort(database.Name)
		server.UsingSSL = viper.GetBool("servers.using-ssl")
		server.AcceptingClients = server.HTTPPortNumber != 0 && acceptsClients
		if server.HTTPPortNumber != 0 {
			startReverseProxy(&server)
		} else {
			logger.Error("No more ports available. Please consider increasing the range of available ports!", zap.String("server", database.Name))
		}
	} else {
		updated := false

		// Check if we are serving this database already
		if server.HTTPPortNumber == 0 {
			// We are not, check if we have any port available now
			server.HTTPPortNumber = assignPort(database.Name)
			if server.HTTPPortNumber != 0 {
				server.AcceptingClients = acceptsClients
				startReverseProxy(&server)
				logger.Info("A port has become available. Assigning port to server.", zap.String("server", database.Name), zap.Int("port", server.HTTPPortNumber))
				updated = true
			}
		}
		if !updated && server.AcceptingClients != acceptsClients {
			server.AcceptingClients = !server.AcceptingClients
			updated = true
		}
		if server.Host != viper.GetString("servers.host-name") {
			server.Host = viper.GetString("servers.host-name")
			updated = true
		}
		if server.IPAddress != NullableString(viper.GetString("servers.ip-v4-address$")) {
			server.IPAddress = NullableString(viper.GetString("servers.ip-v4-address$"))
			updated = true
		}
		if server.IPv6Address != NullableString(viper.GetString("servers.ip-v6-address$")) {
			server.IPv6Address = NullableString(viper.GetString("servers.ip-v6-address$"))
			updated = true
		}
		if !updated {
			return
		}
	}
	server.LastUpdated = time.Now().Format(time.RFC3339)
	activeServersByName[server.Name] = server
	activeServersByPort[server.HTTPPortNumber] = server.Name
}

func removeServer(name string) {
	// Lookup the server and remove it from the list
	server, exists := activeServersByName[name]
	if !exists {
		return
	}

	logger.Info("Terminating server proxy", zap.String("server", server.Name), zap.Int("port", server.HTTPPortNumber))

	// Shut down the server gracefully
	if err := server.httpServer.Shutdown(context.Background()); err != nil {
		logger.Error("Error shutting down server", zap.Error(err), zap.String("server", server.Name), zap.Int("port", server.HTTPPortNumber))
	}

	// Remove the server from the map of active servers
	delete(activeServersByPort, server.HTTPPortNumber)
	delete(activeServersByName, server.Name)
}

// Refresh our collection of servers based on the available databases
func refreshServers() error {
	// Retrieve the list of databases from the tm1 service
	databases, err := listDatabases()
	if err != nil {
		return err
	}

	mu.Lock()         // Lock before starting the refresh
	defer mu.Unlock() // Unlock after we've completed the refresh

	// Update the servers based on the current list of databases, starting
	// by removing any servers representing a database that no longer exists
	var serversToRemove []string
	for _, server := range activeServersByName {
		exists := func() bool {
			for _, database := range databases {
				if server.Name == database.Name {
					return true
				}
			}
			return false
		}()

		if !exists {
			serversToRemove = append(serversToRemove, server.Name)
		}
	}
	for _, serverToRemove := range serversToRemove {
		removeServer(serverToRemove)
	}

	// Now lets make sure that every database is represented by a server
	for _, database := range databases {
		upsertServer(&database)
	}

	return nil
}

func listServers() []Server {
	// Before we return the list of servers refresh it first
	err := refreshServers()
	if err != nil {
		logger.Error("Unable to refresh servers list", zap.Error(err))
	}

	mu.Lock()
	defer mu.Unlock()

	// Return an array with the currently active servers
	servers := []Server{}
	for _, server := range activeServersByName {
		servers = append(servers, server)
	}
	return servers
}

func lookupServer(name string) *Server {
	// Before we look up the requested server refresh the list of servers
	err := refreshServers()
	if err != nil {
		logger.Error("Unable to refresh servers list", zap.Error(err))
	}

	mu.Lock()
	defer mu.Unlock()

	// Look up the server in our collection of servers
	server, exists := activeServersByName[name]
	if !exists {
		return nil
	}
	return &server
}

func shutdownAllServers() {
	mu.Lock()
	defer mu.Unlock()

	// Shut down all active servers gracefully
	for _, server := range activeServersByName {
		if err := server.httpServer.Shutdown(context.Background()); err != nil {
			logger.Error("Error shutting down server", zap.Error(err), zap.String("server", server.Name), zap.Int("port", server.HTTPPortNumber))
		}
	}

	// Clear the maps of active servers
	activeServersByPort = make(map[int]string)
	activeServersByName = make(map[string]Server)

	// Wait for all goroutines to finish
	wg.Wait()
}
