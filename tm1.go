package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/spf13/viper"
)

// Define a struct for our v12 Database type
type ProductVersion struct {
	SemVer string
}

type Replica struct {
	ID    string
	State string
	Role  string
}

type Database struct {
	ID             string
	Name           string
	ProductVersion ProductVersion
	ServiceRootURL string
	Replicas       int
	ActiveReplicas []Replica
}

type DatabasesResponse struct {
	ContextURL string     `json:"@odata.context"`
	Databases  []Database `json:"value"`
}

func listDatabases() ([]Database, error) {
	// Build the request URL requesting the collection of databases
	var reqUrl string
	databasesResourceAndQuery := strings.SplitN(viper.GetString("tm1-v12.databases-url"), "?", 2)
	reqUrl = databasesResourceAndQuery[0] + "?$select=ID,Name,ProductVersion,ServiceRootURL,Replicas&$expand=ActiveReplicas($select=ID,State,Role)"
	if len(databasesResourceAndQuery) > 1 && databasesResourceAndQuery[1] != "" {
		queryAndFragment := strings.SplitN(databasesResourceAndQuery[1], "#", 2)
		reqUrl = reqUrl + "&" + queryAndFragment[0]
		if len(queryAndFragment) > 1 && queryAndFragment[1] != "" {
			reqUrl = reqUrl + "#" + queryAndFragment[1]
		}
	}

	// Create a new HTTP request
	req, err := http.NewRequest("GET", reqUrl, nil)
	if err != nil {
		return nil, err
	}

	// Add the Authorization header to the request
	req.Header.Add("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(viper.GetString("tm1-v12.auth.basic.username")+":"+viper.GetString("tm1-v12.auth.basic.password"))))

	// Send the request using an HTTP client
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Read the response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// Retrieve the list of databases from the body
	var databasesResponse DatabasesResponse
	if err := json.Unmarshal(body, &databasesResponse); err != nil {
		return nil, err
	}

	// Return the list of databases
	return databasesResponse.Databases, nil
}
