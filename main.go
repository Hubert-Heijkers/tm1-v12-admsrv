package main

import (
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"golang.org/x/sys/windows/svc"
)

var logger *zap.Logger
var loggerLevel zap.AtomicLevel

func runServer() {
	// Initialize servers port map and file watcher
	initPortMap()

	// Kick off the reverse proxies for our servers
	go func() {
		err := refreshServers()
		if err != nil {
			logger.Error("Unable to refresh servers list", zap.Error(err))
		}
	}()

	// Create an instance of our own router for the admin server API
	var router admsrvRouter

	httpsPort := viper.GetInt("admsrv.https-port")
	httpPort := viper.GetInt("admsrv.http-port")

	// Start listening to request to our REST API on the specified http and/or https port(s)
	if httpsPort != 0 && httpPort != 0 {
		// Start the HTTPS server in a separate goroutine
		go func() {
			logger.Info("Starting HTTPS server", zap.Int("port", httpsPort))
			if err := http.ListenAndServeTLS(":"+strconv.Itoa(httpsPort), viper.GetString("admsrv.cert-file"), viper.GetString("admsrv.key-file"), &router); err != nil {
				logger.Fatal("HTTPS server failed to start", zap.Error(err))
			}
		}()

		// Start the HTTP server
		logger.Info("Starting HTTP server", zap.Int("port", httpPort))
		if err := http.ListenAndServe(":"+strconv.Itoa(httpPort), &router); err != nil {
			logger.Fatal("HTTP server failed to start", zap.Error(err))
		}
	} else if httpsPort != 0 {
		// Start the HTTPS server
		logger.Info("Starting HTTPS server", zap.Int("port", httpsPort))
		if err := http.ListenAndServeTLS(":"+strconv.Itoa(httpsPort), viper.GetString("admsrv.cert-file"), viper.GetString("admsrv.key-file"), &router); err != nil {
			logger.Fatal("HTTPS server failed to start", zap.Error(err))
		}
	} else if httpPort != 0 {
		// Start the HTTP server
		logger.Info("Starting HTTP server", zap.Int("port", httpPort))
		if err := http.ListenAndServe(":"+strconv.Itoa(httpPort), &router); err != nil {
			logger.Fatal("HTTP server failed to start", zap.Error(err))
		}
	} else {
		logger.Fatal("No HTTP nor HTTPS port specified. Admin service will not be listening to any incoming requests!")
	}
}

type tm1AdminHostService struct{}

func (m *tm1AdminHostService) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (svcSpecificEC bool, exitCode uint32) {
	const cmdsAccepted = svc.AcceptStop | svc.AcceptShutdown
	changes <- svc.Status{State: svc.StartPending}
	go runServer()
	changes <- svc.Status{State: svc.Running, Accepts: cmdsAccepted}
loop:
	for c := range r {
		switch c.Cmd {
		case svc.Interrogate:
			changes <- c.CurrentStatus
		case svc.Stop, svc.Shutdown:
			break loop
		}
	}
	changes <- svc.Status{State: svc.StopPending}

	// Gracefully shutdown all reverse proxies for our active servers
	shutdownAllServers()

	return
}

func runWindowsService(name string) {
	run := svc.Run
	logger.Info("Starting " + name + " Windows service")
	err := run(name, &tm1AdminHostService{})
	if err != nil {
		logger.Error("Service "+name+" failed to start", zap.Error(err))
		return
	}
	logger.Info("Service " + name + " stopped")
}

func setupSignalHandling() {
	// Create a channel to listen for termination signals
	signals := make(chan os.Signal, 1)

	// Notify the channel on SIGINT and SIGTERM
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

	// Start a goroutine to handle received signals
	go func() {
		// Block until a signal is received
		<-signals

		if logger != nil {
			logger.Info("Shutdown signal received, shutting down servers...")
		}

		// Gracefully shutdown all reverse proxies for our active servers
		shutdownAllServers()

		// Exit
		os.Exit(0)
	}()
}

func main() {
	// Install signal handler making sure all proxies shut down gracefully
	setupSignalHandling()

	// Process the config file
	errConfig := initConfig()

	// Open or create the log file
	logFile := viper.GetString("log.file")
	file, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0666)
	if err != nil {
		panic(err)
	}
	defer file.Close()

	// Create a custom encoder config for the JSON encoder
	jsonEncoderConfig := zap.NewProductionEncoderConfig()
	jsonEncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder // Use ISO8601 format for time

	// Configure a file encoder for structured JSON logging
	fileEncoder := zapcore.NewJSONEncoder(jsonEncoderConfig)

	// Create an atomic level to allow dynamic adjustment of the log level
	loggerLevel = zap.NewAtomicLevel()

	// Configure a file write syncer
	fileSyncerCore := zapcore.NewCore(fileEncoder, zapcore.AddSync(file), loggerLevel)

	// Determine if the service is begin started as a Windows service
	isWindowsService, err := svc.IsWindowsService()
	if err != nil {
		// Initialize logging...
		logger = zap.New(fileSyncerCore)
		defer logger.Sync()
		logger.Fatal("Failed to determine if we are running in a windows service", zap.Error(err))

	} else if isWindowsService {
		// Initialize logging...
		logger = zap.New(fileSyncerCore)
		defer logger.Sync()

		// Write any issues with the configuration to the log now that we have it set up
		if errConfig != nil {
			if _, ok := errConfig.(viper.ConfigFileNotFoundError); ok {
				logger.Info("Config file not found. Continuing with default configuration!", zap.Error(errConfig))
			} else {
				logger.Error("Config file could not be read. Continuing with default configuration!", zap.Error(errConfig))
			}
		}

		// Build/validate the configuration now, here, now that the logger is initialized
		buildConfig()

		// Run as a windows service
		runWindowsService(viper.GetString("admsrv.service-name"))

	} else {
		// Initialize logging...
		// Note: running as a console app, log output to console as well, in a more readable form
		consoleEncoder := zapcore.NewConsoleEncoder(zap.NewDevelopmentEncoderConfig())

		// Configure a console write syncer
		consoleSyncerCore := zapcore.NewCore(consoleEncoder, zapcore.AddSync(os.Stdout), loggerLevel)

		// Combine the file and console cores
		core := zapcore.NewTee(
			fileSyncerCore,
			consoleSyncerCore,
		)

		// Build the logger with both cores
		logger = zap.New(core)
		defer logger.Sync()

		// Write any issues with the configuration to the log now that we have it set up
		if errConfig != nil {
			if _, ok := errConfig.(viper.ConfigFileNotFoundError); ok {
				logger.Info("Config file not found. Created config file with default configuration!", zap.Error(errConfig))
			} else {
				logger.Error("Config file could not be read. Continuing with default configuration!", zap.Error(errConfig))
			}
		}

		// Build/validate the configuration now, here, now that the logger is initialized
		buildConfig()

		// Run as a console application
		logger.Info("Starting TM1 v12 admin service")
		runServer()
	}
}
