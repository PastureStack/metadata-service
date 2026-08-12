package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
	yaml "gopkg.in/yaml.v2"
)

const (
	ContentText = 1
	ContentJSON = 2
	ContentYAML = 3

	// The top-level key in the JSON for the default (not client-specific answers)
	DEFAULT_KEY = "default"
)

var (
	VERSION string
	// A key to check for magic traversing of arrays by a string field in them
	// For example, given: { things: [ {name: 'asdf', stuff: 42}, {name: 'zxcv', stuff: 43} ] }
	// Both ../things/0/stuff and ../things/asdf/stuff will return 42 because 'asdf' matched the 'anme' field of one of the 'things'.
	MAGIC_ARRAY_KEYS = []string{"name", "uuid"}
)

// ServerConfig specifies the configuration for the metadata server
type ServerConfig struct {
	answersFilePath string
	listen          string
	listenReload    string
	enableXff       bool

	router             *mux.Router
	reloadRouter       *mux.Router
	metadataController *MetadataController
	reloadChan         chan chan error
}

func main() {
	options, err := parseOptions(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		logrus.Fatal(err)
	}
	if options.showVersion {
		fmt.Println(VERSION)
		return
	}
	if err := appMain(options); err != nil {
		logrus.Fatal(err)
	}
}

type appOptions struct {
	debug               bool
	enableXff           bool
	listen              string
	listenReload        string
	answersFile         string
	logFile             string
	pidFile             string
	subscribe           bool
	reloadIntervalLimit int64
	showVersion         bool
}

func parseOptions(args []string) (appOptions, error) {
	options := appOptions{}
	flags := flag.NewFlagSet("metadata-service", flag.ContinueOnError)
	flags.BoolVar(&options.debug, "debug", false, "enable debug logging")
	flags.BoolVar(&options.enableXff, "xff", false, "trust X-Forwarded-For when selecting client answers")
	flags.StringVar(&options.listen, "listen", ":80", "HTTP listen address")
	flags.StringVar(&options.listenReload, "listen-reload", "127.0.0.1:8112", "reload API listen address")
	flags.StringVar(&options.answersFile, "answers", "./answers.json", "JSON or YAML answers file")
	flags.StringVar(&options.logFile, "log", "", "optional log file")
	flags.StringVar(&options.pidFile, "pid-file", "", "optional PID file")
	flags.BoolVar(&options.subscribe, "subscribe", false, "subscribe to platform configuration events")
	flags.Int64Var(&options.reloadIntervalLimit, "reload-interval-limit", 1000, "minimum reload interval in milliseconds")
	flags.BoolVar(&options.showVersion, "version", false, "print the version and exit")
	if err := flags.Parse(args); err != nil {
		return appOptions{}, err
	}
	if options.reloadIntervalLimit < 1 {
		return appOptions{}, fmt.Errorf("reload-interval-limit must be at least 1 millisecond")
	}
	return options, nil
}

func appMain(options appOptions) error {
	if options.debug {
		logrus.SetLevel(logrus.DebugLevel)
	}

	logFile := options.logFile
	if logFile != "" {
		if output, err := os.OpenFile(logFile, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666); err != nil {
			return fmt.Errorf("open log file: %w", err)
		} else {
			logrus.SetOutput(output)
		}
	}

	pidFile := options.pidFile
	if pidFile != "" {
		logrus.WithField("pid", os.Getpid()).Info("Writing PID file")
		if err := ioutil.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0644); err != nil {
			return fmt.Errorf("write pid file: %w", err)
		}
	}

	sc := NewServerConfig(
		options.answersFile,
		options.listen,
		options.listenReload,
		options.enableXff,
		options.subscribe,
		options.reloadIntervalLimit,
	)

	if err := sc.Start(); err != nil {
		return err
	}

	return sc.RunServer()
}

// platformEnvironment prefers the neutral deployment contract while retaining
// an upgrade-only alias for existing installations. Values are never logged.
func platformEnvironment(primary, compatibilityAlias string) string {
	if value := os.Getenv(primary); value != "" {
		return value
	}
	return os.Getenv(compatibilityAlias)
}

func NewServerConfig(answersFilePath, listen, listenReload string, enableXff bool, subscribe bool, reloadInterval int64) *ServerConfig {
	router := mux.NewRouter()
	reloadRouter := mux.NewRouter()
	reloadChan := make(chan chan error)
	sc := &ServerConfig{
		answersFilePath: answersFilePath,
		listen:          listen,
		listenReload:    listenReload,
		enableXff:       enableXff,
		router:          router,
		reloadRouter:    reloadRouter,
		reloadChan:      reloadChan,
		metadataController: NewMetadataController(subscribe, answersFilePath, reloadInterval,
			platformEnvironment("PLATFORM_ALLOWED_ORIGINS", "CATTLE_ALLOWED_ORIGINS")),
	}
	return sc
}

func (sc *ServerConfig) Start() error {
	logrus.Infof("Starting metadata-service %s", VERSION)
	return sc.metadataController.Start(
		platformEnvironment("PLATFORM_URL", "CATTLE_URL"),
		platformEnvironment("PLATFORM_ACCESS_KEY", "CATTLE_ACCESS_KEY"),
		platformEnvironment("PLATFORM_SECRET_KEY", "CATTLE_SECRET_KEY"),
	)
}

func (sc *ServerConfig) loadAnswersFromFile(file string) error {
	logrus.Info("Loading answers")
	err := sc.metadataController.LoadVersionsFromFile()
	if err == nil {
		logrus.Info("Loaded answers from file")
	} else {
		logrus.WithError(err).Error("Failed to load answers from file")
	}

	return err
}

func (sc *ServerConfig) watchSignals() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGHUP)

	go func() {
		for _ = range c {
			logrus.Info("Received HUP signal")
			sc.reloadChan <- nil
		}
	}()

	go func() {
		for resp := range sc.reloadChan {
			err := sc.loadAnswersFromFile(sc.answersFilePath)
			if resp != nil {
				resp <- err
			}
		}
	}()

}

func (sc *ServerConfig) watchHttp() {
	sc.reloadRouter.HandleFunc("/favicon.ico", http.NotFound)
	sc.reloadRouter.HandleFunc("/v1/reload", sc.httpReload).Methods("POST")

	logrus.Info("Listening for Reload on ", sc.listenReload)
	go http.ListenAndServe(sc.listenReload, sc.reloadRouter)
}

func (sc *ServerConfig) RunServer() error {

	sc.watchSignals()
	sc.watchHttp()

	sc.router.HandleFunc("/favicon.ico", http.NotFound)
	sc.router.HandleFunc("/", sc.root).
		Methods("GET", "HEAD").
		Name("Root")

	sc.router.HandleFunc("/{version}", sc.metadata).
		Methods("GET", "HEAD").
		Name("Version")

	sc.router.HandleFunc("/{version}/{key:.*}", sc.metadata).
		Queries("wait", "true", "value", "{oldValue}").
		Methods("GET", "HEAD").
		Name("Wait")

	sc.router.HandleFunc("/{version}/{key:.*}", sc.metadata).
		Methods("GET", "HEAD").
		Name("Metadata")

	logrus.Info("Listening on ", sc.listen)
	return http.ListenAndServe(sc.listen, sc.router)
}

func (sc *ServerConfig) httpReload(w http.ResponseWriter, req *http.Request) {
	logrus.Debug("Received HTTP reload request")
	respChan := make(chan error)
	sc.reloadChan <- respChan
	err := <-respChan

	if err == nil {
		io.WriteString(w, "OK")
	} else {
		w.WriteHeader(500)
		io.WriteString(w, err.Error())
	}
}

func contentType(req *http.Request) int {
	bestType := ContentText
	bestQuality := -1.0
	for _, candidate := range strings.Split(strings.ToLower(req.Header.Get("Accept")), ",") {
		parts := strings.Split(strings.TrimSpace(candidate), ";")
		quality := 1.0
		for _, parameter := range parts[1:] {
			keyValue := strings.SplitN(strings.TrimSpace(parameter), "=", 2)
			if len(keyValue) == 2 && keyValue[0] == "q" {
				if parsed, err := strconv.ParseFloat(keyValue[1], 64); err == nil {
					quality = parsed
				}
			}
		}
		if quality <= bestQuality {
			continue
		}
		switch parts[0] {
		case "application/json":
			bestType, bestQuality = ContentJSON, quality
		case "application/yaml", "application/x-yaml", "text/yaml", "text/x-yaml":
			bestType, bestQuality = ContentYAML, quality
		case "text/plain", "*/*", "":
			bestType, bestQuality = ContentText, quality
		}
	}
	return bestType
}

func (sc *ServerConfig) root(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")

	logrus.Debug("Metadata root requested")

	answers := sc.metadataController.GetVersions()

	m := make(map[string]interface{})
	for _, k := range answers.Versions() {
		url, err := sc.router.Get("Version").URL("version", k)
		if err == nil {
			m[k] = (*url).String()
		} else {
			logrus.Warn("Unable to construct metadata version URL")
		}
	}

	// If latest isn't in the list, pretend it is
	_, ok := m["latest"]
	if !ok {
		url, err := sc.router.Get("Version").URL("version", "latest")
		if err == nil {
			m["latest"] = (*url).String()
		} else {
			logrus.Warn("Unable to construct latest metadata URL")
		}
	}

	respondSuccess(w, req, m)
}

func (sc *ServerConfig) metadata(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")

	vars := mux.Vars(req)
	clientIp := sc.requestIp(req)

	version := vars["version"]
	wait := mux.CurrentRoute(req).GetName() == "Wait"
	oldValue := vars["oldValue"]
	maxWait, _ := strconv.Atoi(req.URL.Query().Get("maxWait"))

	answers := sc.metadataController.GetVersions()
	_, ok := answers[version]
	if !ok {
		// If a `latest` key is not provided, pick the ASCII-betically highest version and call it that.
		if version == "latest" {
			version = ""
			for _, k := range answers.Versions() {
				if k > version {
					version = k
				}
			}

			logrus.Debug("Resolved the latest metadata version")
		} else {
			respondError(w, req, "Invalid version", http.StatusNotFound)
			return
		}
	}

	path := strings.TrimRight(req.URL.EscapedPath()[1:], "/")
	pathSegments := strings.Split(path, "/")[1:]
	var err error
	for i := 0; err == nil && i < len(pathSegments); i++ {
		pathSegments[i], err = url.QueryUnescape(pathSegments[i])
	}

	if err != nil {
		respondError(w, req, err.Error(), http.StatusBadRequest)
		return
	}

	logrus.WithFields(logrus.Fields{"wait": wait, "max_wait_seconds": maxWait}).Debug("Searching metadata")
	val, ok := sc.metadataController.LookupAnswer(wait, oldValue, version, clientIp, pathSegments, time.Duration(maxWait)*time.Second)

	if ok {
		logrus.Debug("Metadata lookup succeeded")
		respondSuccess(w, req, val)
	} else {
		logrus.Info("Metadata lookup returned no result")
		respondError(w, req, "Not found", http.StatusNotFound)
	}
}

func respondError(w http.ResponseWriter, req *http.Request, msg string, statusCode int) {
	obj := make(map[string]interface{})
	obj["message"] = msg
	obj["type"] = "error"
	obj["code"] = statusCode

	switch contentType(req) {
	case ContentText:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		http.Error(w, msg, statusCode)
	case ContentJSON:
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		bytes, err := json.Marshal(obj)
		if err == nil {
			http.Error(w, string(bytes), statusCode)
		} else {
			http.Error(w, "{\"type\": \"error\", \"message\": \"JSON marshal error\"}", http.StatusInternalServerError)
		}
	case ContentYAML:
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		bytes, err := yaml.Marshal(obj)
		if err == nil {
			http.Error(w, string(bytes), statusCode)
		} else {
			http.Error(w, "type: \"error\"\nmessage: \"JSON marshal error\"", http.StatusInternalServerError)
		}
	}
}

func respondSuccess(w http.ResponseWriter, req *http.Request, val interface{}) {
	switch contentType(req) {
	case ContentText:
		respondText(w, req, val)
	case ContentJSON:
		respondJSON(w, req, val)
	case ContentYAML:
		respondYAML(w, req, val)
	}
}

func respondText(w http.ResponseWriter, req *http.Request, val interface{}) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if val == nil {
		fmt.Fprint(w, "")
		return
	}

	switch v := val.(type) {
	case string, json.Number:
		fmt.Fprint(w, v)
	case uint, uint8, uint16, uint32, uint64, int, int8, int16, int32, int64:
		fmt.Fprintf(w, "%d", v)
	case float64:
		// The default format has extra trailing zeros
		str := strings.TrimRight(fmt.Sprintf("%f", v), "0")
		str = strings.TrimRight(str, ".")
		fmt.Fprint(w, str)
	case bool:
		if v {
			fmt.Fprint(w, "true")
		} else {
			fmt.Fprint(w, "false")
		}
	case map[string]interface{}:
		out := make([]string, len(v))
		i := 0
		for k, vv := range v {
			_, isMap := vv.(map[string]interface{})
			_, isArray := vv.([]interface{})
			if isMap || isArray {
				out[i] = fmt.Sprintf("%s/\n", url.QueryEscape(k))
			} else {
				out[i] = fmt.Sprintf("%s\n", url.QueryEscape(k))
			}
			i++
		}

		sort.Strings(out)
		for _, vv := range out {
			fmt.Fprint(w, vv)
		}
	case []interface{}:
	outer:
		for k, vv := range v {
			vvMap, isMap := vv.(map[string]interface{})
			_, isArray := vv.([]interface{})

			if isMap {
				// If the child is a map and has a "name" property, show index=name ("0=foo")
				for _, magicKey := range MAGIC_ARRAY_KEYS {
					name, ok := vvMap[magicKey]
					if ok {
						fmt.Fprintf(w, "%d=%s\n", k, url.QueryEscape(name.(string)))
						continue outer
					}
				}
			}

			if isMap || isArray {
				// If the child is a map or array, show index ("0/")
				fmt.Fprintf(w, "%d/\n", k)
			} else {
				// Otherwise, show index ("0" )
				fmt.Fprintf(w, "%d\n", k)
			}
		}
	default:
		http.Error(w, "Value is of a type I don't know how to handle", http.StatusInternalServerError)
	}
}

func respondJSON(w http.ResponseWriter, req *http.Request, val interface{}) {
	bytes, err := json.Marshal(val)
	if err == nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Write(bytes)
	} else {
		respondError(w, req, "Error serializing to JSON: "+err.Error(), http.StatusInternalServerError)
	}
}

func respondYAML(w http.ResponseWriter, req *http.Request, val interface{}) {
	bytes, err := yaml.Marshal(val)
	if err == nil {
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		w.Write(bytes)
	} else {
		respondError(w, req, "Error serializing to YAML: "+err.Error(), http.StatusInternalServerError)
	}
}

func (sc *ServerConfig) requestIp(req *http.Request) string {
	if sc.enableXff {
		clientIp := req.Header.Get("X-Forwarded-For")
		if len(clientIp) > 0 {
			return clientIp
		}
	}

	clientIp, _, _ := net.SplitHostPort(req.RemoteAddr)
	return clientIp
}
