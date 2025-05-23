package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

var (
	activeServersByName     = map[string]Server{}
	activeServersByPort     = map[int]string{}
	dictPortByServer        = map[string]int{}
	dictServerByPort        = map[int]string{}
	portLast            int = 0

	mu                  sync.Mutex
	wg                  sync.WaitGroup
	serversWatcher      *fsnotify.Watcher
	ignoreServersUpdate bool = false
	updatePortMapFile   bool = false
)

// File in which we persist our server to port map
const portMapFilePath = "servers.json"

func savePortMapToFile() {
	mu.Lock()
	defer mu.Unlock()

	// Only update if required
	if updatePortMapFile {
		// Convert map to JSON
		jsonPortMap, err := json.MarshalIndent(dictPortByServer, "", "  ")
		if err != nil {
			logger.Error("Error marshalling port map to JSON", zap.Error(err))
			return
		}

		// Mark as an internal update
		ignoreServersUpdate = true

		// Write JSON to file
		err = os.WriteFile(portMapFilePath, jsonPortMap, 0644)
		if err != nil {
			logger.Error("Error writing port map to file", zap.Error(err))
		}

		// Mark the port map file as updated
		updatePortMapFile = false
	}
}

func updatePortMapFromFile() bool {
	mu.Lock()
	defer mu.Unlock()

	// Read file content
	jsonPortMap, err := os.ReadFile(portMapFilePath)
	if err != nil {
		logger.Error("Error reading port map from file", zap.Error(err))
		return false
	}

	// Parse JSON back to map
	var portMap map[string]int
	err = json.Unmarshal(jsonPortMap, &portMap)
	if err != nil {
		logger.Error("Error unmarshalling port map JSON", zap.Error(err))
		return false
	}

	// Restore map
	dictPortByServer = make(map[string]int)
	dictServerByPort = make(map[int]string)
	for server, port := range portMap {
		dictPortByServer[server] = port
		dictServerByPort[port] = server
	}
	return true
}

func watchPortMapFile() {
	for {
		select {
		case event, ok := <-serversWatcher.Events:
			if !ok {
				return
			}

			// Reload the file if it's modified and not an internal write
			if event.Op&(fsnotify.Write|fsnotify.Create) != 0 {
				if ignoreServersUpdate {
					// Skip internal writes
					ignoreServersUpdate = false
					continue
				}

				// Reload the port map from file
				if updatePortMapFromFile() {
					logger.Info("Servers port map reloaded")
				}
			}
		case err, ok := <-serversWatcher.Errors:
			if !ok {
				return
			}
			logger.Error("Servers file watcher error", zap.Error(err))
		}
	}
}

func initPortMap() {
	// Check if file exists
	_, err := os.Stat(portMapFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			// It does not, initialize with empty map so we can watch it
			err = os.WriteFile(portMapFilePath, []byte(`{}`), 0644)
			if err != nil {
				logger.Error("Error writing port map to file", zap.Error(err))
			}
		}
	}

	// Initialize the port map from file
	updatePortMapFromFile()

	// Set up a watcher for the servers file
	serversWatcher, err = fsnotify.NewWatcher()
	if err != nil {
		logger.Error("Failed to initialzing servers file watcher", zap.Error(err))
		return
	}
	if err = serversWatcher.Add(portMapFilePath); err != nil {
		logger.Error("Failed to start watching the servers file", zap.Error((err)))
	}

	// Start watching the servers file
	go watchPortMapFile()
}

func isPortAvailable(port int) bool {
	// Check if the specified port is available by trying to listen on it
	listener, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		// Port is unavailable
		return false
	}
	// It's available, close the listener to release the port
	listener.Close()
	return true
}

func assignPort(server string) int {
	portMin := viper.GetInt("servers.port-range.min")
	portMax := viper.GetInt("servers.port-range.max")

	// Check if this server has had a port assigned to it that is:
	// - not currently being used
	// - and still in bounds of the port range
	if port, exists := dictPortByServer[server]; exists {
		if _, exists := activeServersByPort[port]; !exists {
			if isPortAvailable(port) {
				// Reuse this port
				return port
			} else {
				// Port is not available
				if name, exists := dictServerByPort[port]; exists && name == server {
					delete(dictServerByPort, port)
				}
				delete(dictPortByServer, server)
				updatePortMapFile = true
			}
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
	for portLast++; portLast <= portMax; portLast++ {
		if isPortAvailable(portLast) {
			dictPortByServer[server] = portLast
			dictServerByPort[portLast] = server
			updatePortMapFile = true
			return portLast
		}
	}

	// Must reuse the first port in the range that's not used and available
	for port := portMin; port <= portMax; port++ {
		if _, exists := activeServersByPort[port]; !exists {
			if isPortAvailable(port) {
				if name, exists := dictServerByPort[port]; exists {
					delete(dictPortByServer, name)
				}
				dictPortByServer[server] = port
				dictServerByPort[port] = server
				updatePortMapFile = true
				return port
			}
		}
	}

	// No unused ports left in the port-range
	return 0
}

// responseRecorder wraps the http.ResponseWriter to capture response status and size
type responseRecorder struct {
	http.ResponseWriter
	statusCode   int
	responseSize int64
}

// WriteHeader captures the HTTP status code
func (rr *responseRecorder) WriteHeader(statusCode int) {
	rr.statusCode = statusCode
	rr.ResponseWriter.WriteHeader(statusCode)
}

// Write captures the size of the data written
func (rr *responseRecorder) Write(b []byte) (int, error) {
	size, err := rr.ResponseWriter.Write(b)
	rr.responseSize += int64(size)
	return size, err
}

func logRequestResponse(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Log request details
		startTime := time.Now()

		// Wrap the response writer to capture the status code and response body size
		wrappedWriter := &responseRecorder{ResponseWriter: w, statusCode: http.StatusOK}

		// Call the next handler, which could be another middleware or the final handler
		next.ServeHTTP(wrappedWriter, r)

		// Log response details
		if logger.Level() == zap.DebugLevel {
			logger.Debug("Request Details",
				zap.String("Method", r.Method),
				zap.String("URL", r.URL.Path),
				zap.Any("Query", r.URL.Query()),
				zap.Int("Status", wrappedWriter.statusCode),
				zap.Int("Content-Length", int(wrappedWriter.responseSize)),
				zap.Duration("Duration", time.Since(startTime)))
		}
	})
}

func startPAReverseProxy() {
	// Parse the specified PA URL we are proxying here - TODO: IF WE KEEP THIS MAKE THIS DYNAMIC!
	target := "http://wsl-rhel"
	targetURL, err := url.Parse(target)
	if err != nil {
		logger.Error("Unable to start PA proxy, invalid URL", zap.Error(err), zap.String("url", target))
		return
	}

	// Create a new reverse proxy targeting the targetURL
	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	// Define a custom error handler to log requests targeting the API that weren't handled
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		logger.Error("Error processing PA endpoint", zap.String("path", r.URL.Path), zap.Error(err))
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
	}

	// Now that we have initiated a reverse proxy handler for this database, start listening to the port associated to it
	// TODO: IF WE KEEP THIS MAKE THE PORT DYNAMIC AS WELL
	proxyPortNumber := 5555
	httpServer := &http.Server{
		Addr:      ":" + strconv.Itoa(proxyPortNumber),
		Handler:   logRequestResponse(proxy),
		TLSConfig: &tls.Config{},
	}

	logger.Info("Starting PA proxy", zap.Int("port", proxyPortNumber), zap.String("redirect-url", target))

	// Increment the WaitGroup before starting the server goroutine
	go func() {
		/*
			// Using SSL?
			if server.UsingSSL {
				if err := server.httpServer.ListenAndServeTLS(viper.GetString("servers.cert-file"), viper.GetString("servers.key-file")); err != http.ErrServerClosed {
					logger.Error("Proxy, using SSL, failed to start", zap.Error(err), zap.String("server", server.Name), zap.Int("port", server.HTTPPortNumber), zap.String("servers.cert-file", viper.GetString("servers.cert-file")), zap.String("servers.key-file", viper.GetString("servers.key-file")))
				}
			} else {
		*/
		if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
			logger.Error("PA proxy failed to start", zap.Error(err), zap.Int("port", proxyPortNumber))
		}
		/*
			}
		*/
	}()
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

	// Create a new reverse proxy targeting the targetURL
	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	// Modify the request before it is forwarded
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		// Rewrite the paths wer are willing to handle before calling the original director
		if strings.HasPrefix(req.URL.Path, "/api/v1/") {
			// The proxy targets the service root of the database, make the path relative to that
			req.URL.Path = req.URL.Path[7:]
		} else if req.URL.Path == "/api/logout" {
			// Convert to POST request targetting /ActiveSession/tm1.Close instead
			req.Method = "POST"
			req.URL.Path = "/ActiveSession/tm1.Close"
			req.Header.Set("Content-Type", "application/json")
			newBody := `{}`
			req.Body = io.NopCloser(bytes.NewBufferString(newBody))
			req.ContentLength = int64(len(newBody))
		} else {
			return
		}

		// Call the original director to have the request URL rewritten
		originalDirector(req)

		/*
			// Ensure cookies are forwarded to the backend.
			logger.Debug("Request", zap.String("Url", req.URL.Path), zap.Strings("Cookie", req.Header["Cookie"]))
		*/

	}

	// Define a custom error handler to log requests targeting the API that weren't handled
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {

		// For optimal performance of the proxy we'll deal with request targetting our old
		// internal API (which should have been removed a decade ago already) here instead.
		if strings.HasPrefix(r.URL.Path, "/api/internal/") {
			segments := strings.Split(r.URL.Path[14:], "/")
			if len(segments) == 2 && (segments[0] == "v1.1" || segments[0] == "v1") {
				switch segments[1] {
				case "capabilities":
					handleInternalCapabilitiesResource(w, r)
					return
				case "configuration":
					handleInternalConfigurationResource(w, r)
					return
				case "sandboxes":
					handleInternalSandboxesResource(w, r)
					return
				}
			}
		}

		// Log errors for requests targetting our REST APIs
		if strings.HasPrefix(r.URL.Path, "/api/") {
			logger.Error("Error processing API endpoint", zap.String("path", r.URL.Path), zap.Error(err))
			http.Error(w, "Bad Request", http.StatusBadRequest)
		} else {
			http.NotFound(w, r)
		}
	}

	// Now that we have initiated a reverse proxy handler for this database, start listening to the port associated to it
	server.httpServer = &http.Server{
		Addr:      ":" + strconv.Itoa(server.HTTPPortNumber),
		Handler:   logRequestResponse(proxy),
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
				if server.Name == database.Name && database.Replicas > 0 {
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
		if database.Replicas > 0 {
			upsertServer(&database)
		}
	}

	// Make sure any changes made to the port map get persisted in the servers file
	go func() {
		savePortMapToFile()
	}()
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
