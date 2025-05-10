package main

import (
	"net/http"
)

func handleInternalCapabilitiesResource(w http.ResponseWriter, r *http.Request) {

	// Only allow GET requests
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// The capabilities list is simply a fixed collection of strings
	data := `[ { "name" : "RunServerExplorer", "permissions" : [ { "name" : "Execute", "policy" : "" } ] }, { "name" : "UsePersonalWorkspaceWritebackMode", "permissions" : [ { "name" : "Execute", "policy" : "" } ] }, { "name" : "UseSandbox", "permissions" : [ { "name" : "Execute", "policy" : "Grant" } ] }, { "name" : "ManageDataReservation", "permissions" : [ { "name" : "Execute", "policy" : "Grant" } ] }, { "name" : "DataReservationOverride", "permissions" : [ { "name" : "Execute", "policy" : "Grant" } ] }, { "name" : "Consolidation TypeIn Spreading", "permissions" : [ { "name" : "Execute", "policy" : "Grant" } ] }, { "name" : "Allow Spreading", "permissions" : [ { "name" : "Execute", "policy" : "Grant" } ] }, { "name" : "Allow Export as Text", "permissions" : [ { "name" : "Execute", "policy" : "Grant" } ] } ]`

	// Write the response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(data))
}

func handleInternalConfigurationResource(w http.ResponseWriter, r *http.Request) {

	// Only allow GET requests
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// TODO: Configuration can be deduced from /api/v1/Configuration or from /api/v1/ActiveConfiguration if we desparately want to include the IdleConnectionTimeOutSeconds
	data := `{ "ServerName" : "Planning Sample", "AdminHost" : "", "ProductVersion" : "12.4.5", "PortNumber" : 0, "ClientMessagePortNumber" : 0, "HTTPPortNumber" : 12555, "IntegratedSecurityMode" : 1, "SecurityMode" : "Basic", "ClientCAMURI" : "", "AllowSeparateNandCRules" : 0, "DistributedOutputDir" : "", "DisableSandboxing" : false, "JobQueuing" : false, "ForceReevaluationOfFeedersForFedCellsOnDataChange" : false, "DataBaseDirectory" : "c:\\users\\037583788\\w\\bin\\tm1\\data\\plansamp", "UnicodeUpperLowerCase" : true, "IdleConnectionTimeOutSeconds" : 0 }`

	// Write the response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(data))
}

func handleInternalSandboxesResource(w http.ResponseWriter, r *http.Request) {

	// Only allow GET requests
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// TODO: Build the sandboxes response, for now presume empty collection
	data := `[]`

	// Write the response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(data))
}
