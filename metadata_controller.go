package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PastureStack/metadata-service/internal/platformevents"
)

var credentialIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,255}$`)

type metadataSource struct {
	key         string
	url         string
	accessKey   string
	secretKey   string
	local       bool
	generator   *Generator
	subscriber  *Subscriber
	versions    Versions
	credentials []Credential
	version     string
	active      atomic.Bool

	mu sync.RWMutex
}

func newMetadataSource(rawURL, accessKey, secretKey string, local bool, answersFile string) *metadataSource {
	source := &metadataSource{
		key:       accessKey,
		url:       rawURL,
		accessKey: accessKey,
		secretKey: secretKey,
		local:     local,
		generator: NewGenerator(local, answersFile),
	}
	source.active.Store(true)
	return source
}

func (source *metadataSource) setData(versions Versions, credentials []Credential, version string) {
	source.mu.Lock()
	source.versions = versions
	source.credentials = append([]Credential(nil), credentials...)
	source.version = version
	source.mu.Unlock()
}

func (source *metadataSource) snapshot() (Versions, []Credential) {
	source.mu.RLock()
	defer source.mu.RUnlock()
	return source.versions, append([]Credential(nil), source.credentials...)
}

func (source *metadataSource) fullSnapshot() (Versions, []Credential, string) {
	source.mu.RLock()
	defer source.mu.RUnlock()
	return source.versions, append([]Credential(nil), source.credentials...), source.version
}

type MetadataController struct {
	subscribe      bool
	answersFile    string
	reloadInterval int64
	allowedRaw     string
	allowedOrigins map[string]struct{}

	mu       sync.RWMutex
	cond     *sync.Cond
	local    *metadataSource
	external map[string]*metadataSource
	versions Versions
	version  string

	reconcileMu sync.Mutex
}

func NewMetadataController(subscribe bool, answersFile string, reloadInterval int64, allowedOrigins string) *MetadataController {
	controller := &MetadataController{
		subscribe:      subscribe,
		answersFile:    answersFile,
		reloadInterval: reloadInterval,
		allowedRaw:     allowedOrigins,
		allowedOrigins: parseAllowedOrigins(allowedOrigins),
		external:       make(map[string]*metadataSource),
		version:        "0",
	}
	controller.cond = sync.NewCond(&controller.mu)
	return controller
}

func parseAllowedOrigins(raw string) map[string]struct{} {
	result := make(map[string]struct{})
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if origin, err := canonicalAllowedOrigin(item); err == nil {
			result[origin] = struct{}{}
		}
	}
	return result
}

func validateAllowedOrigins(raw string) error {
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, err := canonicalAllowedOrigin(item); err != nil {
			return fmt.Errorf("invalid allowed platform origin: %w", err)
		}
	}
	return nil
}

func canonicalAllowedOrigin(raw string) (string, error) {
	origin, err := canonicalOrigin(raw)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", fmt.Errorf("allowed platform origin must not contain a path")
	}
	return origin, nil
}

func canonicalOrigin(raw string) (string, error) {
	return platformevents.CanonicalOrigin(raw)
}

func (controller *MetadataController) Start(rawURL, accessKey, secretKey string) error {
	if err := validateAllowedOrigins(controller.allowedRaw); err != nil {
		return err
	}
	controller.local = newMetadataSource(rawURL, accessKey, secretKey, true, controller.answersFile)
	versions, credentials, version, err := controller.local.generator.LoadVersionsFromFile(true)
	if err != nil {
		return fmt.Errorf("load local metadata answers: %w", err)
	}
	if versions != nil {
		controller.local.setData(versions, credentials, version)
		if err := controller.reconcile(); err != nil {
			return err
		}
	}

	if !controller.subscribe {
		return nil
	}
	if rawURL == "" {
		return fmt.Errorf("platform URL is required in subscription mode")
	}
	return controller.startSource(controller.local)
}

func (controller *MetadataController) startSource(source *metadataSource) error {
	subscriber, err := NewSubscriber(source.url, source.accessKey, source.secretKey, source.generator, controller.reloadInterval,
		func(versions Versions, credentials []Credential, version string) error {
			return controller.applySourceData(source, versions, credentials, version)
		})
	if err != nil {
		return err
	}
	source.subscriber = subscriber
	return subscriber.Subscribe()
}

func (controller *MetadataController) applySourceData(source *metadataSource, versions Versions, credentials []Credential, version string) error {
	if !source.local {
		credentials = nil
	}
	previousVersions, previousCredentials, previousVersion := source.fullSnapshot()
	source.setData(versions, credentials, version)
	if err := controller.reconcileFor(source); err != nil {
		source.setData(previousVersions, previousCredentials, previousVersion)
		return err
	}
	return nil
}

func (controller *MetadataController) LoadVersionsFromFile() error {
	if controller.local == nil {
		return fmt.Errorf("metadata controller is not started")
	}
	versions, credentials, version, err := controller.local.generator.LoadVersionsFromFile(false)
	if err != nil {
		return err
	}
	return controller.applySourceData(controller.local, versions, credentials, version)
}

func (controller *MetadataController) reconcile() error {
	return controller.reconcileFor(nil)
}

func (controller *MetadataController) reconcileFor(trigger *metadataSource) error {
	controller.reconcileMu.Lock()
	defer controller.reconcileMu.Unlock()
	if trigger != nil {
		if !trigger.active.Load() {
			return fmt.Errorf("metadata source is no longer active")
		}
		if !trigger.local {
			current, ok := controller.external[trigger.key]
			if !ok || current != trigger {
				return fmt.Errorf("metadata source has been replaced")
			}
		}
	}

	localVersions, credentials := controller.local.snapshot()
	desired := make(map[string]Credential)
	if len(credentials) > 256 {
		return fmt.Errorf("external metadata source limit exceeded")
	}
	for _, credential := range credentials {
		if !credentialIdentifierPattern.MatchString(credential.PublicValue) {
			return fmt.Errorf("external metadata credential identifier is invalid")
		}
		if len(credential.URL) > 2048 || len(credential.SecretValue) > 4096 {
			return fmt.Errorf("external metadata credential exceeds the supported size")
		}
		if err := controller.validateExternalSource(credential.URL); err != nil {
			return err
		}
		if _, exists := desired[credential.PublicValue]; exists {
			return fmt.Errorf("external metadata credential identifier is duplicated")
		}
		desired[credential.PublicValue] = credential
	}

	var toStop []*metadataSource
	for key, existing := range controller.external {
		credential, ok := desired[key]
		if !ok || credential.URL != existing.url || credential.SecretValue != existing.secretKey {
			existing.active.Store(false)
			toStop = append(toStop, existing)
			delete(controller.external, key)
		}
	}
	for _, source := range toStop {
		if source.subscriber != nil {
			source.subscriber.Cancel()
		}
	}

	keys := make([]string, 0, len(desired))
	for key := range desired {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, exists := controller.external[key]; exists {
			continue
		}
		credential := desired[key]
		answersFile := externalAnswersFile(controller.answersFile, key)
		source := newMetadataSource(credential.URL, credential.PublicValue, credential.SecretValue, false, answersFile)
		controller.external[key] = source
		if controller.subscribe {
			if err := controller.startSource(source); err != nil {
				source.active.Store(false)
				delete(controller.external, key)
				return fmt.Errorf("start external metadata source: %w", err)
			}
		}
	}

	externalVersions := make([]Versions, 0, len(controller.external))
	for _, key := range keys {
		if source, ok := controller.external[key]; ok {
			versions, _ := source.snapshot()
			if versions != nil {
				externalVersions = append(externalVersions, versions)
			}
		}
	}
	mergeVersion := ""
	if len(externalVersions) > 0 {
		mergeVersion = newMetadataVersion()
	}
	merged := MergeVersions(localVersions, externalVersions, mergeVersion)
	controller.mu.Lock()
	controller.versions = merged
	controller.version = versionFromAnswers(merged)
	controller.cond.Broadcast()
	controller.mu.Unlock()
	return nil
}

func (controller *MetadataController) validateExternalSource(rawURL string) error {
	origin, err := canonicalOrigin(rawURL)
	if err != nil {
		return err
	}
	if controller.local.url != "" {
		localOrigin, err := canonicalOrigin(controller.local.url)
		if err != nil {
			return err
		}
		if origin == localOrigin {
			return nil
		}
	}
	if _, ok := controller.allowedOrigins[origin]; !ok {
		return fmt.Errorf("external metadata origin is not allowlisted")
	}
	return nil
}

func externalAnswersFile(base, key string) string {
	directory := filepath.Dir(base)
	name := filepath.Base(base)
	digest := sha256.Sum256([]byte(key))
	return filepath.Join(directory, name+".external."+hex.EncodeToString(digest[:12])+".json")
}

func newMetadataVersion() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(value)
}

func MergeVersions(local Versions, external []Versions, version string) Versions {
	merged := cloneVersions(local)
	if len(merged) == 0 {
		return merged
	}
	if version == "" {
		version = highestNumericVersion(merged)
	}
	environments := make([]interface{}, 0, len(external))
	for _, versions := range external {
		if defaults, ok := versions[version3][DEFAULT_KEY].(map[string]interface{}); ok {
			environments = append(environments, cloneValue(defaults))
		}
	}
	for _, metadataVersion := range versionList {
		for _, value := range merged[metadataVersion] {
			data, ok := value.(map[string]interface{})
			if !ok {
				continue
			}
			if metadataVersion == version3 {
				data["environments"] = cloneValue(environments)
			}
			data["version"] = version
		}
	}
	merged["latest"] = merged[version3]
	return merged
}

func highestNumericVersion(versions Versions) string {
	highest := int64(-1)
	for _, metadataVersion := range versionList {
		for _, value := range versions[metadataVersion] {
			data, ok := value.(map[string]interface{})
			if !ok {
				continue
			}
			candidate, ok := data["version"].(string)
			if !ok {
				continue
			}
			parsed, err := strconv.ParseInt(candidate, 10, 64)
			if err == nil && parsed > highest {
				highest = parsed
			}
		}
	}
	if highest < 0 {
		return "0"
	}
	return strconv.FormatInt(highest, 10)
}

func cloneVersions(source Versions) Versions {
	if source == nil {
		return nil
	}
	return cloneValue(source).(Versions)
}

func cloneValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case Versions:
		result := make(Versions, len(typed))
		for key, child := range typed {
			result[key] = cloneValue(child).(Answers)
		}
		return result
	case Answers:
		result := make(Answers, len(typed))
		for key, child := range typed {
			result[key] = cloneValue(child)
		}
		return result
	case map[string]interface{}:
		result := make(map[string]interface{}, len(typed))
		for key, child := range typed {
			result[key] = cloneValue(child)
		}
		return result
	case []interface{}:
		result := make([]interface{}, len(typed))
		for index, child := range typed {
			result[index] = cloneValue(child)
		}
		return result
	default:
		return typed
	}
}

func (controller *MetadataController) GetVersions() Versions {
	controller.mu.RLock()
	defer controller.mu.RUnlock()
	return controller.versions
}

func (controller *MetadataController) SetStaticVersions(versions Versions) {
	controller.mu.Lock()
	controller.versions = versions
	controller.cond.Broadcast()
	controller.mu.Unlock()
}

func (controller *MetadataController) LookupAnswer(wait bool, oldValue, version, ip string, path []string, maxWait time.Duration) (interface{}, bool) {
	if !wait {
		versions := controller.GetVersions()
		return versions.Matching(version, ip, path)
	}
	if maxWait == 0 {
		maxWait = time.Minute
	}
	if maxWait > 2*time.Minute {
		maxWait = 2 * time.Minute
	}
	deadline := time.Now().Add(maxWait)
	for {
		controller.mu.Lock()
		versions := controller.versions
		value, ok := versions.Matching(version, ip, path)
		if time.Now().After(deadline) || (ok && fmt.Sprint(value) != oldValue) {
			controller.mu.Unlock()
			return value, ok
		}
		remaining := time.Until(deadline)
		timer := time.AfterFunc(remaining, func() {
			controller.mu.Lock()
			controller.cond.Broadcast()
			controller.mu.Unlock()
		})
		controller.cond.Wait()
		timer.Stop()
		controller.mu.Unlock()
	}
}

func (controller *MetadataController) Stop() {
	controller.reconcileMu.Lock()
	var subscribers []*Subscriber
	if controller.local != nil {
		controller.local.active.Store(false)
		if controller.local.subscriber != nil {
			subscribers = append(subscribers, controller.local.subscriber)
		}
	}
	for _, source := range controller.external {
		source.active.Store(false)
		if source.subscriber != nil {
			subscribers = append(subscribers, source.subscriber)
		}
	}
	controller.reconcileMu.Unlock()

	for _, subscriber := range subscribers {
		subscriber.Unsubscribe()
	}
}
