package main

import (
	log "github.com/sirupsen/logrus"
	"strconv"
	"strings"
)

func (answers *Versions) Versions() []string {
	out := make([]string, 0, len(*answers))
	for k := range *answers {
		out = append(out, k)
	}

	return out
}

func (answers *Versions) Matching(version string, ip string, path []string) (interface{}, bool) {
	var out interface{}

	all, ok := (*answers)[version]
	if ok == false {
		return nil, false
	}

	// Try the client's IP
	thisIp, ok := all[ip]
	if ok == false {
		// Try the default key because no entry for the client existed
		if ip == DEFAULT_KEY {
			return nil, false
		} else {
			log.Debug("No client-specific answers; using the default answer set")
			return answers.Matching(version, DEFAULT_KEY, path)
		}
	}

	if len(path) == 0 {
		return thisIp, true
	}

	out, ok = valueForPath(&thisIp, path)
	if ok {
		return out, true
	} else {
		// Try the path all lowercased for case-insensitivity
		var lowerPath []string
		for _, k := range path {
			lowerPath = append(lowerPath, strings.ToLower(k))
		}
		log.Debug("Metadata path not found; retrying case-insensitively")
		out, ok = valueForPath(&thisIp, lowerPath)
		if ok {
			return out, true
		}
	}

	return nil, false
}

func valueForPath(in *interface{}, path []string) (interface{}, bool) {
	out := *in

	for _, key := range path {
		valid := false

		switch v := out.(type) {
		case []interface{}:
			idx, err := strconv.ParseInt(key, 10, 64)
			if err == nil {
				// If the part is a number, treat it like an array index
				if idx >= 0 && idx < int64(len(v)) {
					out = v[idx]
					valid = true
				}
			} else {
				// Otherwise maybe it's the name of a child map
				vAry, _ := out.([]interface{})
			outer:
				for childK, childV := range vAry {
					childMap, ok := childV.(map[string]interface{})
					if ok {
						for _, magicKey := range MAGIC_ARRAY_KEYS {
							if childMap[magicKey] == key {
								out = vAry[childK]
								valid = true
								break outer
							}
						}
					}
				}
			}

		case map[string]interface{}:
			out, valid = v[key]

		default:
			log.Debug("Metadata path reached an unsupported value type")
		}

		if valid == false {
			return nil, false
		}
	}

	return out, true
}
