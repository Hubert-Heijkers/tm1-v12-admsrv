package main

import (
	"net"
	"regexp"
	"strings"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

func initConfig() error {
	// Set up configuration (Viper)
	viper.SetConfigName("config") // Name of config file (without extension)
	viper.SetConfigType("json")   // Specify the format (e.g., JSON, YAML)
	viper.AddConfigPath(".")      // Look for config in the current directory

	// Set default values
	viper.SetDefault("admsrv.service-name", "tm1-v12-admsrv") // The name used for the windows service
	viper.SetDefault("admsrv.http-port", 5895)                // HTTP port for the admin host to listen on
	viper.SetDefault("admsrv.https-port", 5898)               // HTTPS port for the admin host to listen on
	viper.SetDefault("admsrv.cert-file", "./cert.pem")        // Path to SSL certificate file
	viper.SetDefault("admsrv.key-file", "./key.pem")          // Path to SSL key file

	viper.SetDefault("tm1-v12.databases-url", "http://localhost:4444/tm1/api/v1/Databases") // TM1 v12 databases collection URL
	viper.SetDefault("tm1-v12.database-url-template", nil)                                  // TM1 v12 database URL template (default: "<<databases-url>>('{{.database}}')")
	viper.SetDefault("tm1-v12.auth.basic.username", nil)                                    // The user name of the user logging in
	viper.SetDefault("tm1-v12.auth.basic.password", nil)                                    // The password of the user logging in

	viper.SetDefault("servers.host-name", "localhost")  // The host name returned as the Host in every server entity ("" => null)
	viper.SetDefault("servers.ip-v4-address", nil)      // The IP v4 address returned in every server entity ("" => null)
	viper.SetDefault("servers.ip-v6-address", nil)      // The IP v6 address returned in every server entity ("" => null)
	viper.SetDefault("servers.port-range.min", 9601)    // The lower bound of the port range used by the servers
	viper.SetDefault("servers.port-range.max", 9659)    // The upper bound of the port range used by the servers
	viper.SetDefault("servers.using-ssl", false)        // Boolean indicating if we expect our clients to use SSL
	viper.SetDefault("servers.cert-file", "./cert.pem") // Path to SSL certificate file used by the reverse proxy
	viper.SetDefault("servers.key-file", "./key.pem")   // Path to SSL key file used by the reverse proxy

	viper.SetDefault("log.file", "./tm1-v12-admsrv.log") // Log file name
	viper.SetDefault("log.level", "info")                // Log level (fatal, error, warning, info and debug)

	// Watch the config file and re-read it on change
	viper.OnConfigChange(func(e fsnotify.Event) {
		err := viper.ReadInConfig()
		if err != nil {
			if logger != nil {
				logger.Error("Unable to reload configuration", zap.Error(err))
			}
		} else {
			if logger != nil {
				logger.Info("Configuration reloaded")
			}
			buildConfig()
		}
	})
	viper.WatchConfig()

	// Read the config file
	err := viper.ReadInConfig()
	if err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			// First time running this service? Create config file with default values
			viper.SafeWriteConfig()
		} else {
			return err
		}
	}
	return nil
}

func buildConfig() {
	// Update the log level
	switch viper.GetString("log.level") {
	case "fatal":
		loggerLevel.SetLevel(zap.FatalLevel)
	case "error":
		loggerLevel.SetLevel(zap.ErrorLevel)
	case "warning":
		loggerLevel.SetLevel(zap.WarnLevel)
	case "info":
		loggerLevel.SetLevel(zap.InfoLevel)
	case "debug":
		loggerLevel.SetLevel(zap.DebugLevel)
	default:
		logger.Info("Unknown log level, please specify fatal, error, warning, info or debug, defaulting to info level", zap.String("log.level", viper.GetString("log.level")))
		viper.Set("log.level", nil)
		loggerLevel.SetLevel(zap.InfoLevel)
	}

	// Resolve IP address for host just in case some client only looks at IP address
	hostName := viper.GetString("servers.host-name")
	if hostName != "" && viper.GetString("servers.ip-v4-address") == "" && viper.GetString("servers.ip-v6-address") == "" {
		if ips, err := net.LookupIP(hostName); err == nil {
			viper.Set("servers.ip-v4-address$", nil)
			viper.Set("servers.ip-v6-address$", nil)
			for _, ip := range ips {
				if ip.To4() != nil {
					if viper.GetString("servers.ip-v4-address$") == "" {
						viper.Set("servers.ip-v4-address$", ip.String())
					}
				} else {
					if viper.GetString("servers.ip-v6-address$") == "" {
						viper.Set("servers.ip-v6-address$", ip.String())
					}
				}
			}
		} else {
			viper.Set("servers.ip-v4-address$", viper.GetString("servers.ip-v4-address"))
			viper.Set("servers.ip-v4-address$", viper.GetString("servers.ip-v4-address"))
		}
	} else {
		viper.Set("servers.ip-v4-address$", viper.GetString("servers.ip-v4-address"))
		viper.Set("servers.ip-v4-address$", viper.GetString("servers.ip-v4-address"))
	}

	// Validate the databases URL
	databasesResourceAndQuery := strings.Split(viper.GetString("tm1-v12.databases-url"), "?")
	protoAndResource := strings.SplitN(databasesResourceAndQuery[0], "://", 2)
	if len(protoAndResource) != 2 || (protoAndResource[0] != "http" && protoAndResource[0] != "https") {
		logger.Fatal("Invalid Databases url specified: protocol missing or invalid", zap.String("tm1-v12.databases-url", viper.GetString("tm1-v12.databases-url")))
	}
	hostAndPathSegments := strings.Split(protoAndResource[1], "/")
	if len(hostAndPathSegments) < 2 || len(databasesResourceAndQuery) > 2 {
		logger.Fatal("Invalid Databases url specified", zap.String("tm1-v12.databases-url", viper.GetString("tm1-v12.databases-url")))
	}
	if hostAndPathSegments[len(hostAndPathSegments)-1] != "Databases" {
		logger.Fatal("Invalid Databases url specified: path should end with 'Databases' segment", zap.String("tm1-v12.databases-url", viper.GetString("tm1-v12.databases-url")))
	}

	// Validate the database URL template if one provided
	databaseUrlTemplate := viper.GetString("tm1-v12.database-url-template")
	if databaseUrlTemplate == "" {
		databaseUrlTemplate = databasesResourceAndQuery[0] + "('{{.database}}')"
		if len(databasesResourceAndQuery) == 2 {
			databaseUrlTemplate = databaseUrlTemplate + "?" + databasesResourceAndQuery[1]
		}
		viper.Set("tm1-v12.database-url-template$", databaseUrlTemplate)
	} else {
		// Regular expression to match variables in a template
		templateVarRegex := regexp.MustCompile(`{{([^\/]+)}}`)
		matches := templateVarRegex.FindStringSubmatch(databaseUrlTemplate)

		// Check it only contains one variable and that the variable is named 'database'
		if len(matches) != 2 || matches[1] != "database" {
			logger.Fatal("Database URL template invalid. Template should contain exactly one variable named 'database' as in 'Databases('{{database}}')", zap.String("tm1-v12.database-url-template", databaseUrlTemplate))
		}

		// Use the regex to replace {{database}} with {{.database}}
		viper.Set("tm1-v12.database-url-template$", templateVarRegex.ReplaceAllString(databaseUrlTemplate, "{{.$1}}"))
	}

	// Valid the port range specified
	portMin := viper.GetInt("servers.port-range.min")
	portMax := viper.GetInt("servers.port-range.max")
	if portMin <= 0 || portMax > 65535 || portMin > portMax {
		logger.Error("No valid port range specified! Falling back to using default port range [9601:9659]!", zap.Int("servers.port-range.min", portMin), zap.Int("servers.port-range.max", portMax))
		viper.Set("servers.port-range.min", nil)
		viper.Set("servers.port-range.max", nil)
	}
}
