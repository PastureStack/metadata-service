package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"

	log "github.com/sirupsen/logrus"
	yaml "gopkg.in/yaml.v2"
)

const maxAnswersFileBytes int64 = 64 << 20

func ParseAnswers(path string) (Versions, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			log.Warn("Answers file was not found")
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()

	content, err := io.ReadAll(io.LimitReader(file, maxAnswersFileBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > maxAnswersFileBytes {
		return nil, fmt.Errorf("answers file exceeds %d bytes", maxAnswersFileBytes)
	}

	var savedDelta MetadataDelta
	if err := json.Unmarshal(content, &savedDelta); err == nil && len(savedDelta.Data) > 0 {
		generator := NewGenerator(true, path)
		delta, _, err := generator.generateDelta(bytes.NewReader(savedDelta.Data), false)
		if err != nil {
			return nil, err
		}
		versions, _, err := generator.GenerateAnswers(delta)
		return versions, err
	}

	versions := Versions{}
	if err := json.Unmarshal(content, &versions); err == nil && len(versions) > 0 {
		return versions, nil
	}

	var yamlValue interface{}
	if err := yaml.Unmarshal(content, &yamlValue); err != nil {
		return nil, fmt.Errorf("decode answers as JSON or YAML: %w", err)
	}
	normalized, err := normalizeYAMLValue(yamlValue)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(encoded, &versions); err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("answers file has no versions")
	}
	return versions, nil
}

func normalizeYAMLValue(value interface{}) (interface{}, error) {
	switch typed := value.(type) {
	case map[interface{}]interface{}:
		result := make(map[string]interface{}, len(typed))
		for key, child := range typed {
			stringKey, ok := key.(string)
			if !ok {
				return nil, fmt.Errorf("answers file contains a non-string map key")
			}
			normalized, err := normalizeYAMLValue(child)
			if err != nil {
				return nil, err
			}
			result[stringKey] = normalized
		}
		return result, nil
	case []interface{}:
		result := make([]interface{}, len(typed))
		for index, child := range typed {
			normalized, err := normalizeYAMLValue(child)
			if err != nil {
				return nil, err
			}
			result[index] = normalized
		}
		return result, nil
	default:
		return value, nil
	}
}
